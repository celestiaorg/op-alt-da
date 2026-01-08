package celestia

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	awskeyring "github.com/celestiaorg/aws-kms-keyring"
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

// CelestiaBlobID field sizes in bytes.
const (
	// HeightSize is the size of the Height field (uint64).
	HeightSize = 8
	// CommitmentSize is the size of the Commitment field (shares commitment hash).
	CommitmentSize = 32
	// ShareOffsetSize is the size of the ShareOffset field (uint32).
	ShareOffsetSize = 4
	// ShareSizeSize is the size of the ShareSize field (uint32).
	ShareSizeSize = 4

	// CompactBlobIDSize is the total size of a compact blob ID (height + commitment).
	CompactBlobIDSize = HeightSize + CommitmentSize // 40 bytes
	// FullBlobIDSize is the total size of a full blob ID (height + commitment + offset + size).
	FullBlobIDSize = HeightSize + CommitmentSize + ShareOffsetSize + ShareSizeSize // 48 bytes
)

// CelestiaBlobID represents the on-chain identifier for a Celestia blob.
type CelestiaBlobID struct {
	isCompact   bool
	Height      uint64
	Commitment  []byte
	ShareOffset uint32
	ShareSize   uint32
}

// SetCompact sets whether the blob ID should use compact format.
func (c *CelestiaBlobID) SetCompact(compact bool) {
	c.isCompact = compact
}

// IsCompact returns whether the blob ID uses compact format.
func (c *CelestiaBlobID) IsCompact() bool {
	return c.isCompact
}

// Validate checks that the CelestiaBlobID has valid field values.
func (c *CelestiaBlobID) Validate() error {
	if c.Commitment == nil {
		return fmt.Errorf("commitment cannot be nil")
	}
	if len(c.Commitment) != CommitmentSize {
		return fmt.Errorf("commitment must be %d bytes, got %d", CommitmentSize, len(c.Commitment))
	}
	// Height of 0 is technically valid (genesis), so we don't validate it
	return nil
}

// MarshalBinary serializes the CelestiaBlobID struct into a byte slice.
// Returns an error if validation fails.
func (c *CelestiaBlobID) MarshalBinary() ([]byte, error) {
	// Validate before marshaling to catch errors early
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("marshal validation failed: %w", err)
	}

	if c.isCompact {
		id := make([]byte, CompactBlobIDSize)
		binary.LittleEndian.PutUint64(id[0:HeightSize], c.Height)
		copy(id[HeightSize:HeightSize+CommitmentSize], c.Commitment)
		return id, nil
	}

	// Full format includes ShareOffset and ShareSize
	id := make([]byte, FullBlobIDSize)

	binary.LittleEndian.PutUint64(id[0:HeightSize], c.Height)
	copy(id[HeightSize:HeightSize+CommitmentSize], c.Commitment)
	binary.LittleEndian.PutUint32(id[HeightSize+CommitmentSize:HeightSize+CommitmentSize+ShareOffsetSize], c.ShareOffset)
	binary.LittleEndian.PutUint32(id[HeightSize+CommitmentSize+ShareOffsetSize:FullBlobIDSize], c.ShareSize)

	return id, nil
}

// UnmarshalBinary deserializes a byte slice into a CelestiaBlobID struct.
// Supports both compact (40 bytes) and full (48 bytes) formats.
func (c *CelestiaBlobID) UnmarshalBinary(data []byte) error {
	// Check minimum length for compact format
	if len(data) < CompactBlobIDSize {
		return fmt.Errorf("invalid ID length: expected at least %d bytes (compact format), got %d", CompactBlobIDSize, len(data))
	}

	// Parse height
	c.Height = binary.LittleEndian.Uint64(data[0:HeightSize])

	// Parse commitment
	c.Commitment = make([]byte, CommitmentSize)
	copy(c.Commitment, data[HeightSize:HeightSize+CommitmentSize])

	// Check if we have full format data
	if len(data) >= FullBlobIDSize {
		c.ShareOffset = binary.LittleEndian.Uint32(data[HeightSize+CommitmentSize : HeightSize+CommitmentSize+ShareOffsetSize])
		c.ShareSize = binary.LittleEndian.Uint32(data[HeightSize+CommitmentSize+ShareOffsetSize : FullBlobIDSize])
		c.isCompact = false
	} else {
		// Compact format - no share offset/size
		c.ShareOffset = 0
		c.ShareSize = 0
		c.isCompact = true
	}

	// Validate the unmarshaled data
	if err := c.Validate(); err != nil {
		return fmt.Errorf("unmarshal validation failed: %w", err)
	}

	return nil
}

const VersionByte = 0x0c

type TxClientConfig struct {
	DefaultKeyName     string
	KeyringBackend     string // Backend type: remote - "awskms", local - "test", "file", "os", "kwallet", "pass", "keychain", "memory"
	KeyringPath        string
	CoreGRPCAddr       string
	CoreGRPCTLSEnabled bool
	CoreGRPCAuthToken  string
	P2PNetwork         string
	TxWorkerAccounts   int // 0=immediate, 1=queued single, >1=parallel workers

	// Keyring backend-specific configuration
	AWSKMSConfig       *awskeyring.Config
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
// The provided context is used for client initialization and should be the application's
// root context to allow cancellation during startup.
func NewCelestiaStore(ctx context.Context, cfg RPCClientConfig) (*CelestiaStore, error) {
	var blobClient blobAPI.Module
	var err error
	if cfg.TxClientConfig != nil {
		blobClient, err = initTxClient(ctx, cfg)
	} else {
		blobClient, err = initRPCClient(ctx, cfg)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to initialize celestia client: %w", err)
	}

	namespace, err := libshare.NewNamespaceFromBytes(cfg.Namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to parse namespace: %w", err)
	}

	return &CelestiaStore{
		Log:           log.New(),
		Client:        blobClient,
		GetTimeout:    time.Minute,
		Namespace:     namespace,
		CompactBlobID: cfg.CompactBlobID,
	}, nil
}

func initKeyring(ctx context.Context, cfg *RPCClientConfig) (keyring.Keyring, error) {
	keyname := cfg.TxClientConfig.DefaultKeyName
	if keyname == "" {
		keyname = "my_celes_key"
	}

	backend := cfg.TxClientConfig.KeyringBackend
	if backend == "" {
		backend = keyring.BackendTest
	}

	var kr keyring.Keyring
	var err error
	switch backend {
	case "awskms":
		if cfg.TxClientConfig.AWSKMSConfig == nil {
			return nil, fmt.Errorf("AWS KMS config is required when using awskms backend")
		}
		kr, err = awskeyring.NewKMSKeyring(ctx, keyname, *cfg.TxClientConfig.AWSKMSConfig)
	default:
		kr, err = txClient.KeyringWithNewKey(txClient.KeyringConfig{
			KeyName:     keyname,
			BackendName: backend,
		}, cfg.TxClientConfig.KeyringPath)
	}
	return kr, err
}

// initTxClient initializes a transaction client for Celestia.
// The provided context is used for client initialization and allows cancellation during startup.
func initTxClient(ctx context.Context, cfg RPCClientConfig) (blobAPI.Module, error) {
	kr, err := initKeyring(ctx, &cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize keyring: %w", err)
	}

	// Configure client
	// TxWorkerAccounts controls parallel transaction submission:
	//   - 0: Immediate submission (no queue, default)
	//   - 1: Synchronous submission (queued, single signer)
	//   - >1: Parallel submission (queued, multiple worker accounts)
	config := txClient.Config{
		ReadConfig: txClient.ReadConfig{
			BridgeDAAddr: cfg.URL,
			DAAuthToken:  cfg.AuthToken,
			EnableDATLS:  cfg.TLSEnabled,
		},
		SubmitConfig: txClient.SubmitConfig{
			DefaultKeyName:   keyname,
			Network:          p2p.Network(cfg.TxClientConfig.P2PNetwork),
			TxWorkerAccounts: cfg.TxClientConfig.TxWorkerAccounts,
			CoreGRPCConfig: txClient.CoreGRPCConfig{
				Addr:       cfg.TxClientConfig.CoreGRPCAddr,
				TLSEnabled: cfg.TxClientConfig.CoreGRPCTLSEnabled,
				AuthToken:  cfg.TxClientConfig.CoreGRPCAuthToken,
			},
		},
	}

	// Log submission mode
	switch {
	case cfg.TxClientConfig.TxWorkerAccounts > 1:
		log.Info("Parallel submission mode enabled", "tx_worker_accounts", cfg.TxClientConfig.TxWorkerAccounts)
	case cfg.TxClientConfig.TxWorkerAccounts == 1:
		log.Info("Synchronous submission mode enabled (queued, single signer)")
	default:
		log.Info("Immediate submission mode (default, no queue)")
	}
	celestiaClient, err := txClient.New(ctx, config, kr)
	if err != nil {
		return nil, fmt.Errorf("failed to create tx client: %w", err)
	}
	return celestiaClient.Blob, nil
}

// initRPCClient initializes an RPC client for Celestia.
// The provided context is used for client initialization and allows cancellation during startup.
func initRPCClient(ctx context.Context, cfg RPCClientConfig) (blobAPI.Module, error) {
	celestiaClient, err := client.NewClient(ctx, cfg.URL, cfg.AuthToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create rpc client: %w", err)
	}
	return &celestiaClient.Blob, nil
}

func (d *CelestiaStore) Get(ctx context.Context, key []byte) ([]byte, error) {
	d.Log.Info("celestia: blob request", "id", hex.EncodeToString(key))

	// Validate minimum length before slicing
	if len(key) < 2 {
		return nil, fmt.Errorf("invalid commitment: too short (need at least 2 bytes, got %d)", len(key))
	}

	// Validate version bytes:
	// key[0] = 0x01 (Generic Commitment type from optimism alt-da)
	// key[1] = VersionByte (0x0c for Celestia)
	if key[0] != 0x01 {
		return nil, fmt.Errorf("unsupported commitment type: 0x%02x (expected 0x01)", key[0])
	}
	if key[1] != VersionByte {
		return nil, fmt.Errorf("unsupported DA version: 0x%02x (expected 0x%02x)", key[1], VersionByte)
	}

	ctx, cancel := context.WithTimeout(ctx, d.GetTimeout)
	defer cancel()

	var blobID CelestiaBlobID
	// Skip first 2 bytes which are frame version and altda version
	if err := blobID.UnmarshalBinary(key[2:]); err != nil {
		return nil, fmt.Errorf("failed to unmarshal blob ID: %w", err)
	}

	log.Debug("Retrieving blob with commitment",
		"blobID.Commitment", hex.EncodeToString(blobID.Commitment),
		"blobID.Height", blobID.Height)

	blob, err := d.Client.Get(ctx, blobID.Height, d.Namespace, blobID.Commitment)
	if err != nil {
		// Check if error indicates blob not found
		if isBlobNotFoundError(err) {
			return nil, altda.ErrNotFound
		}
		return nil, fmt.Errorf("celestia: failed to resolve frame: %w", err)
	}
	if blob == nil {
		return nil, altda.ErrNotFound
	}
	return blob.Data(), nil
}

// isBlobNotFoundError checks if the error from Celestia client indicates blob not found.
func isBlobNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "blob: not found") ||
		strings.Contains(errStr, "blob not found") ||
		strings.Contains(errStr, "not found")
}

func (d *CelestiaStore) Put(ctx context.Context, data []byte) ([]byte, []byte, error) {
	id, blobData, err := d.submitBlob(ctx, data)
	if err != nil {
		return nil, nil, err
	}

	d.Log.Info("celestia: blob successfully submitted", "id", hex.EncodeToString(id))
	commitment := altda.NewGenericCommitment(append([]byte{VersionByte}, id...))
	return commitment.Encode(), blobData, nil
}

func (d *CelestiaStore) CreateCommitment(data []byte) ([]byte, error) {
	b, err := blob.NewBlob(libshare.ShareVersionZero, d.Namespace, data, nil)
	if err != nil {
		return nil, err
	}
	return b.Commitment, nil
}

// submitBlob submits a blob to Celestia and returns the marshaled blob ID.
func (d *CelestiaStore) submitBlob(ctx context.Context, data []byte) ([]byte, []byte, error) {
	b, err := blob.NewBlob(libshare.ShareVersionZero, d.Namespace, data, nil)
	if err != nil {
		return nil, nil, err
	}

	height, err := d.Client.Submit(ctx, []*blob.Blob{b}, state.NewTxConfig())
	if err != nil {
		return nil, nil, err
	}

	blobID := CelestiaBlobID{
		Height:     height,
		Commitment: b.Commitment,
		isCompact:  d.CompactBlobID,
	}

	// For compact format, re-fetch to get share index and size
	if d.CompactBlobID {
		b, err = d.Client.Get(ctx, height, d.Namespace, b.Commitment)
		if err != nil {
			return nil, nil, err
		}
		size, err := b.Length()
		if err != nil {
			return nil, nil, err
		}
		blobID.ShareOffset = uint32(b.Index())
		blobID.ShareSize = uint32(size)
	}

	id, err := blobID.MarshalBinary()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal blob ID: %w", err)
	}

	return id, b.Data(), nil
}
