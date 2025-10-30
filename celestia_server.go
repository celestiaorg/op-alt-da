package celestia

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/celestiaorg/celestia-node/blob"
	libshare "github.com/celestiaorg/go-square/v2/share"
	s3 "github.com/celestiaorg/op-alt-da/s3"
	altda "github.com/ethereum-optimism/optimism/op-alt-da"
	"github.com/ethereum-optimism/optimism/op-service/httputil"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "op_altda_request_duration_seconds",
			Help:    "Duration of requests",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method"},
	)
	blobSize = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "op_altda_blob_size_bytes",
			Help:    "Size of blobs",
			Buckets: []float64{1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 8388608}, // 1k to 8M
		},
	)
	inclusionHeight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "op_altda_inclusion_height",
			Help: "Inclusion height of blobs",
		},
	)
)

type CelestiaServer struct {
	log        log.Logger
	endpoint   string
	store      *CelestiaStore
	s3Store    *s3.S3Store
	tls        *httputil.ServerTLSConfig
	httpServer *http.Server
	listener   net.Listener

	cache        bool
	fallback     bool
	cacheLock    sync.RWMutex
	fallbackLock sync.RWMutex

	metricsEnabled bool
	metricsPort    int
}

func NewCelestiaServer(host string, port int, store *CelestiaStore, s3Store *s3.S3Store, fallback bool, cache bool, metricsEnabled bool, metricsPort int, log log.Logger) *CelestiaServer {
	endpoint := net.JoinHostPort(host, strconv.Itoa(port))
	server := &CelestiaServer{
		log:      log,
		endpoint: endpoint,
		store:    store,
		s3Store:  s3Store,
		httpServer: &http.Server{
			Addr: endpoint,
		},
		fallback:       fallback,
		cache:          cache,
		metricsEnabled: metricsEnabled,
		metricsPort:    metricsPort,
	}
	if metricsEnabled {
		prometheus.MustRegister(requestDuration, blobSize, inclusionHeight)
	}
	return server
}

func (d *CelestiaServer) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/get/", d.HandleGet)
	mux.HandleFunc("/put/", d.HandlePut)
	mux.HandleFunc("/put", d.HandlePut)

	d.httpServer.Handler = mux

	listener, err := net.Listen("tcp", d.endpoint)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	d.listener = listener

	d.endpoint = listener.Addr().String()
	errCh := make(chan error, 1)
	go func() {
		if d.tls != nil {
			if err := d.httpServer.ServeTLS(d.listener, "", ""); err != nil {
				errCh <- err
			}
		} else {
			if err := d.httpServer.Serve(d.listener); err != nil {
				errCh <- err
			}
		}
	}()

	if d.metricsEnabled {
		go func() {
			metricsAddr := fmt.Sprintf(":%d", d.metricsPort)
			d.log.Info("Starting metrics server", "addr", metricsAddr)
			if err := http.ListenAndServe(metricsAddr, promhttp.Handler()); err != nil {
				d.log.Error("Metrics server failed", "err", err)
			}
		}()
	}

	// verify that the server comes up
	tick := time.NewTimer(10 * time.Millisecond)
	defer tick.Stop()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server failed: %w", err)
	case <-tick.C:
		return nil
	}
}

func (d *CelestiaServer) HandleGet(w http.ResponseWriter, r *http.Request) {
	if d.metricsEnabled {
		timer := prometheus.NewTimer(requestDuration.WithLabelValues("get"))
		defer timer.ObserveDuration()
	}

	d.log.Debug("GET", "url", r.URL)

	route := path.Dir(r.URL.Path)
	if route != "/get" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	key := path.Base(r.URL.Path)
	comm, err := hexutil.Decode(key)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// 1 read blob from cache if enabled
	if d.cache {
		cachedData, cacheErr := d.multiSourceRead(r.Context(), comm, d.store.Namespace, false)
		if cacheErr == nil && cachedData != nil {
			// Successfully got data from cache
			if _, err := w.Write(cachedData); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
			}
			return
		}
	}
	// 2 read blob from Celestia
	celestiaData, err := d.store.Get(r.Context(), comm)
	if err != nil {
		if errors.Is(err, altda.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}

	if celestiaData != nil {
		if _, err := w.Write(celestiaData); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}
	// 3 fallback
	if d.fallback {
		fallbackData, fallbackErr := d.multiSourceRead(r.Context(), comm, d.store.Namespace, true)
		if fallbackErr == nil && fallbackData != nil {
			if _, err := w.Write(fallbackData); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
			}
			return
		}
	}
	// if we goy here then no data was found
	w.WriteHeader(http.StatusInternalServerError)
}

func (d *CelestiaServer) HandlePut(w http.ResponseWriter, r *http.Request) {
	if d.metricsEnabled {
		timer := prometheus.NewTimer(requestDuration.WithLabelValues("put"))
		defer timer.ObserveDuration()
	}

	d.log.Debug("PUT", "url", r.URL)

	route := path.Base(r.URL.Path)
	if route != "put" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	input, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	commitment, blobData, err := d.store.Put(r.Context(), input)
	if err != nil {
		key := hexutil.Encode(commitment)
		d.log.Info("Failed to store commitment to the DA server", "err", err, "key", key)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Observe metrics for blob size and height
	if d.metricsEnabled {
		blobSize.Observe(float64(len(input)))
		var blobID CelestiaBlobID
		// Skip first 2 bytes which are frame version and altda version
		if err := blobID.UnmarshalBinary(commitment[2:]); err != nil {
			d.log.Error("Failed to unmarshal blob ID", "err", err)
		}
		inclusionHeight.Set(float64(blobID.Height))
	}

	if d.cache || d.fallback {
		err = d.handleRedundantWrites(r.Context(), commitment, blobData)
		if err != nil {
			d.log.Error("Failed to write to redundant backends", "err", err)
		}
	}

	if _, err := w.Write(commitment); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func (b *CelestiaServer) Endpoint() string {
	return b.listener.Addr().String()
}

func (b *CelestiaServer) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = b.httpServer.Shutdown(ctx)
	return nil
}

// multiSourceRead ... reads from a set of backends and returns the first successfully read blob
func (b *CelestiaServer) multiSourceRead(ctx context.Context, commitment []byte, namespace libshare.Namespace, fallback bool) ([]byte, error) {

	if fallback {
		b.fallbackLock.RLock()
		defer b.fallbackLock.RUnlock()
	} else {
		b.cacheLock.RLock()
		defer b.cacheLock.RUnlock()
	}

	var blobID CelestiaBlobID
	// Skip first 2 bytes which are frame version and altda version
	if err := blobID.UnmarshalBinary(commitment[2:]); err != nil {
		return nil, fmt.Errorf("failed to unmarshal blob ID: %w", err)
	}

	key := crypto.Keccak256(commitment)
	b.log.Debug("s3 key", "key", hex.EncodeToString(key))
	ctx, cancel := context.WithTimeout(ctx, b.s3Store.Timeout())
	data, err := b.s3Store.Get(ctx, key)
	defer cancel()
	if err != nil {
		b.log.Warn("Failed to read from redundant target S3", "err", err, "key", key)
		return nil, errors.New("no data found in any redundant backend")
	}

	blob, err := blob.NewBlob(libshare.ShareVersionZero, namespace, data, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create blob: %w", err)
	}

	if err != nil || !bytes.Equal(blob.Commitment, blobID.Commitment) {
		return nil, fmt.Errorf("celestia: invalid commitment: commit=%x commitment=%x err=%w", blob.Commitment, blobID.Commitment, err)
	}

	return data, nil
}

// handleRedundantWrites ... writes to both sets of backends (i.e, fallback, cache)
// and returns an error if NONE of them succeed
// NOTE: multi-target set writes are done at once to avoid re-invocation of the same write function at the same
// caller step for different target sets vs. reading which is done conditionally to segment between a cached read type
// vs a fallback read type
func (b *CelestiaServer) handleRedundantWrites(ctx context.Context, commitment []byte, value []byte) error {
	b.cacheLock.RLock()
	b.fallbackLock.RLock()

	defer func() {
		b.cacheLock.RUnlock()
		b.fallbackLock.RUnlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, b.s3Store.Timeout())
	key := crypto.Keccak256(commitment)
	err := b.s3Store.Put(ctx, key, value)
	defer cancel()
	if err != nil {
		b.log.Warn("Failed to write to redundant s3 target", "err", err, "timeout", b.s3Store.Timeout(), "key", key)
	}

	return nil
}
