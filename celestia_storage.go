package celestia

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	txClient "github.com/celestiaorg/celestia-node/api/client"
	"github.com/celestiaorg/celestia-node/api/rpc/client"
	"github.com/celestiaorg/celestia-node/blob"
	blobAPI "github.com/celestiaorg/celestia-node/nodebuilder/blob"
	"github.com/celestiaorg/celestia-node/nodebuilder/p2p"
	"github.com/celestiaorg/celestia-node/state"
	libshare "github.com/celestiaorg/go-square/v3/share"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	altda "github.com/ethereum-optimism/optimism/op-alt-da"
	"github.com/ethereum/go-ethereum/log"
)

// CelestiaBlobID represents the on-chain identifier for a Celestia blob.
type CelestiaBlobID struct {
	isCompact   bool
	Height      uint64
	Commitment  []byte
	ShareOffset uint32
	ShareSize   uint32
}

// MarshalBinary serializes the CelestiaBlobID struct into a byte slice.
func (c *CelestiaBlobID) MarshalBinary() ([]byte, error) {
	if c.isCompact {
		id := make([]byte, 8+32)
		binary.LittleEndian.PutUint64(id[0:8], c.Height)
		copy(id[8:40], c.Commitment)
		return id, nil
	}

	// Calculate the total length of the marshaled ID
	// 8 bytes for Height + 32 bytes for Commitment + 4 bytes for ShareOffset + 4 bytes for ShareSize
	id := make([]byte, 8+32+4+4)

	binary.LittleEndian.PutUint64(id[0:8], c.Height)
	copy(id[8:40], c.Commitment) // Commitment is 32 bytes
	binary.LittleEndian.PutUint32(id[40:44], c.ShareOffset)
	binary.LittleEndian.PutUint32(id[44:48], c.ShareSize)

	return id, nil
}

// UnmarshalBinary deserializes a byte slice into a CelestiaBlobID struct.
func (c *CelestiaBlobID) UnmarshalBinary(data []byte) error {
	// Expected length: 8 bytes for Height + 32 bytes for Commitment + 4 bytes for ShareOffset + 4 bytes for ShareSize
	expectedLen := 8 + 32 + 4 + 4
	if len(data) < expectedLen {
		// Expected length: 8 bytes for Height + 32 bytes for Commitment
		expectedLen = 8 + 32
		if len(data) < expectedLen {
			return fmt.Errorf("invalid ID length: expected at least %d bytes, got %d", expectedLen, len(data))
		}
		c.Height = binary.LittleEndian.Uint64(data[0:8])
		c.Commitment = make([]byte, 32)
		copy(c.Commitment, data[8:40]) // Commitment is 32 bytes
		return nil
	}

	c.Height = binary.LittleEndian.Uint64(data[0:8])
	c.Commitment = make([]byte, 32)
	copy(c.Commitment, data[8:40]) // Commitment is 32 bytes
	c.ShareOffset = binary.LittleEndian.Uint32(data[40:44])
	c.ShareSize = binary.LittleEndian.Uint32(data[44:48])

	return nil
}

const VersionByte = 0x0c

type TxClientConfig struct {
	DefaultKeyName     string
	KeyringPath        string
	CoreGRPCAddr       string
	CoreGRPCTLSEnabled bool
	CoreGRPCAuthToken  string
	P2PNetwork         string
}

type RPCClientConfig struct {
	URL            string
	TLSEnabled     bool
	AuthToken      string
	Namespace      []byte
	CompactBlobID  bool
	TxClientConfig *TxClientConfig
}

// CelestiaStore implements DAStorage with celestia backend
type CelestiaStore struct {
	Log           log.Logger
	GetTimeout    time.Duration
	Namespace     libshare.Namespace
	Client        blobAPI.Module
	CompactBlobID bool
}

// NewCelestiaStore returns a celestia store.
func NewCelestiaStore(cfg RPCClientConfig) *CelestiaStore {
	var blobClient blobAPI.Module
	var err error
	if cfg.TxClientConfig != nil {
		blobClient, err = initTxClient(cfg)
	} else {
		blobClient, err = initRPCClient(cfg)
	}
	if err != nil {
		log.Crit("failed to initialize celestia client", "err", err)
	}
	namespace, err := libshare.NewNamespaceFromBytes(cfg.Namespace)
	if err != nil {
		log.Crit("failed to parse namespace", "err", err)
	}
	return &CelestiaStore{
		Log:           log.New(),
		Client:        blobClient,
		GetTimeout:    time.Minute,
		Namespace:     namespace,
		CompactBlobID: cfg.CompactBlobID,
	}
}

// initTxClient initializes a transaction client for Celestia.
func initTxClient(cfg RPCClientConfig) (blobAPI.Module, error) {
	keyname := cfg.TxClientConfig.DefaultKeyName
	if keyname == "" {
		keyname = "my_celes_key"
	}
	kr, err := txClient.KeyringWithNewKey(txClient.KeyringConfig{
		KeyName:     keyname,
		BackendName: keyring.BackendTest,
	}, cfg.TxClientConfig.KeyringPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create keyring: %w", err)
	}

	// Configure client
	config := txClient.Config{
		ReadConfig: txClient.ReadConfig{
			BridgeDAAddr: cfg.URL,
			DAAuthToken:  cfg.AuthToken,
			EnableDATLS:  cfg.TLSEnabled,
		},
		SubmitConfig: txClient.SubmitConfig{
			DefaultKeyName: cfg.TxClientConfig.DefaultKeyName,
			Network:        p2p.Network(cfg.TxClientConfig.P2PNetwork),
			CoreGRPCConfig: txClient.CoreGRPCConfig{
				Addr:       cfg.TxClientConfig.CoreGRPCAddr,
				TLSEnabled: cfg.TxClientConfig.CoreGRPCTLSEnabled,
				AuthToken:  cfg.TxClientConfig.CoreGRPCAuthToken,
			},
		},
	}
	ctx := context.Background()
	celestiaClient, err := txClient.New(ctx, config, kr)
	if err != nil {
		return nil, fmt.Errorf("failed to create tx client: %w", err)
	}
	return celestiaClient.Blob, nil
}

// initRPCClient initializes an RPC client for Celestia.
func initRPCClient(cfg RPCClientConfig) (blobAPI.Module, error) {
	celestiaClient, err := client.NewClient(context.Background(), cfg.URL, cfg.AuthToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create rpc client: %w", err)
	}
	return &celestiaClient.Blob, nil
}

func (d *CelestiaStore) Get(ctx context.Context, key []byte) ([]byte, error) {
	d.Log.Info("celestia: blob request", "id", hex.EncodeToString(key))
	ctx, cancel := context.WithTimeout(context.Background(), d.GetTimeout)
	defer cancel()

	var blobID CelestiaBlobID
	// Skip first 2 bytes which are frame version and altda version
	if err := blobID.UnmarshalBinary(key[2:]); err != nil {
		return nil, fmt.Errorf("failed to unmarshal blob ID: %w", err)
	}

	blob, err := d.Client.Get(ctx, blobID.Height, d.Namespace, blobID.Commitment)
	if err != nil {
		return nil, fmt.Errorf("celestia: failed to resolve frame: %w", err)
	}
	if blob == nil {
		return nil, fmt.Errorf("celestia: failed to resolve frame: nil blob")
	}
	return blob.Data(), nil
}

func (d *CelestiaStore) Put(ctx context.Context, data []byte) ([]byte, error) {
	b, err := blob.NewBlob(libshare.ShareVersionZero, d.Namespace, data, nil)
	if err != nil {
		return nil, err
	}

	height, err := d.Client.Submit(ctx, []*blob.Blob{b}, state.NewTxConfig())
	if err != nil {
		return nil, err
	}

	var blobID CelestiaBlobID
	if d.CompactBlobID {
		// Re-fetch the blob to get its index and length
		b, err = d.Client.Get(ctx, height, d.Namespace, b.Commitment)
		if err != nil {
			return nil, err
		}

		size, err := b.Length()
		if err != nil {
			return nil, err
		}

		blobID = CelestiaBlobID{
			Height:      height,
			Commitment:  b.Commitment,
			ShareOffset: uint32(b.Index()),
			ShareSize:   uint32(size),
			isCompact:   d.CompactBlobID,
		}
	} else {
		blobID = CelestiaBlobID{
			Height:     height,
			Commitment: b.Commitment,
			isCompact:  d.CompactBlobID,
		}
	}

	id, err := blobID.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal blob ID: %w", err)
	}

	d.Log.Info("celestia: blob successfully submitted", "id", hex.EncodeToString(id))
	commitment := altda.NewGenericCommitment(append([]byte{VersionByte}, id...))
	return commitment.Encode(), nil
}

func (d *CelestiaStore) CreateCommitment(data []byte) ([]byte, error) {
	b, err := blob.NewBlob(libshare.ShareVersionZero, d.Namespace, data, nil)
	if err != nil {
		return nil, err
	}
	return b.Commitment, nil
}
