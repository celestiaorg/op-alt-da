package celestia

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	s3 "github.com/celestiaorg/op-plasma-celestia/s3"
	plasma "github.com/ethereum-optimism/optimism/op-plasma"
	"github.com/ethereum-optimism/optimism/op-service/rpc"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
)

type CelestiaServer struct {
	log        log.Logger
	endpoint   string
	store      *CelestiaStore
	s3Store    *s3.S3Store
	tls        *rpc.ServerTLSConfig
	httpServer *http.Server
	listener   net.Listener

	cache        bool
	fallback     bool
	cacheLock    sync.RWMutex
	fallbackLock sync.RWMutex
}

func NewCelestiaServer(host string, port int, store *CelestiaStore, s3Store *s3.S3Store, fallback bool, cache bool, log log.Logger) *CelestiaServer {
	endpoint := net.JoinHostPort(host, strconv.Itoa(port))
	return &CelestiaServer{
		log:      log,
		endpoint: endpoint,
		store:    store,
		s3Store:  s3Store,
		httpServer: &http.Server{
			Addr: endpoint,
		},
		fallback: fallback,
		cache:    cache,
	}
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
	responseSent := false
	if d.cache {
		d.log.Debug("Retrieving data from cached backends")
		input, err := d.multiSourceRead(r.Context(), comm, false)
		if err == nil {
			responseSent = true
			if _, err := w.Write(input); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
	}
	// 2 read blob from Celestia
	input, err := d.store.Get(r.Context(), comm)
	if err != nil && errors.Is(err, plasma.ErrNotFound) {
		responseSent = true
		w.WriteHeader(http.StatusNotFound)
		return
	} else {
		if input == nil {
			responseSent = true
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		responseSent = true
		if _, err := w.Write(input); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	//3 fallback
	if d.fallback && err != nil {
		input, err = d.multiSourceRead(r.Context(), comm, true)
		if err != nil {
			d.log.Error("Failed to read from fallback", "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	} else if !responseSent {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (d *CelestiaServer) HandlePut(w http.ResponseWriter, r *http.Request) {
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

	commitment, err := d.store.Put(r.Context(), input)
	if err != nil {
		key := hexutil.Encode(commitment)
		d.log.Info("Failed to store commitment to the DA server", "err", err, "key", key)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if d.cache || d.fallback {
		err = d.handleRedundantWrites(r.Context(), commitment, input)
		if err != nil {
			log.Error("Failed to write to redundant backends", "err", err)
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
func (b *CelestiaServer) multiSourceRead(ctx context.Context, commitment []byte, fallback bool) ([]byte, error) {

	if fallback {
		b.fallbackLock.RLock()
		defer b.fallbackLock.RUnlock()
	} else {
		b.cacheLock.RLock()
		defer b.cacheLock.RUnlock()
	}

	key := crypto.Keccak256(commitment)
	ctx, cancel := context.WithTimeout(ctx, b.s3Store.Timeout())
	data, err := b.s3Store.Get(ctx, key)
	defer cancel()
	if err != nil {
		b.log.Warn("Failed to read from redundant target S3", "err", err, "key", key)
		return nil, errors.New("no data found in any redundant backend")
	}

	commit, err := b.store.CreateCommitment(data)
	if err != nil || !bytes.Equal(commit, commitment[10:]) {
		return nil, fmt.Errorf("celestia: invalid commitment: commit=%x commitment=%x err=%w", commit, commitment[10:], err)
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
