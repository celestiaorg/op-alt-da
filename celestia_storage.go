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
	sdk "github.com/cosmos/cosmos-sdk/types"
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

const (
	// VersionByte is the Celestia-specific version byte used in commitment encoding.
	//
	// Commitment Format (follows Optimism Alt-DA specification):
	//   [alt-da_type_byte][celestia_version_byte][celestia_blob_id]
	//   └─────────────────┴──────────────────────┴────────────────────
	//    Added by altda     VersionByte (0x0c)     Height+Commitment+...
	//    GenericCommitment
	//
	// The two-layer versioning scheme enables:
	// 1. Alt-DA type byte: Multi-provider interoperability (Celestia, EigenDA, Avail, etc.)
	// 2. Celestia version byte: Format evolution within Celestia (currently 0x0c)
	//
	// When constructing commitments manually, use:
	//   altda.NewGenericCommitment(append([]byte{VersionByte}, blobID...)).Encode()
	//
	// In practice, commitments should be obtained from Put() responses rather than
	// constructed manually, as the format may evolve.
	VersionByte = 0x0c

	// Blob ID layout constants
	HeightSize      = 8
	CommitmentSize  = 32
	ShareOffsetSize = 4
	ShareSizeSize   = 4
	CompactIDSize   = HeightSize + CommitmentSize
	FullIDSize      = HeightSize + CommitmentSize + ShareOffsetSize + ShareSizeSize
)

// MarshalBinary serializes the CelestiaBlobID struct into a byte slice.
func (c *CelestiaBlobID) MarshalBinary() ([]byte, error) {
	if c.isCompact {
		id := make([]byte, CompactIDSize)
		binary.LittleEndian.PutUint64(id[0:HeightSize], c.Height)
		copy(id[HeightSize:CompactIDSize], c.Commitment)
		return id, nil
	}

	// Calculate the total length of the marshaled ID
	id := make([]byte, FullIDSize)

	binary.LittleEndian.PutUint64(id[0:HeightSize], c.Height)
	copy(id[HeightSize:HeightSize+CommitmentSize], c.Commitment) // Commitment is 32 bytes
	binary.LittleEndian.PutUint32(id[HeightSize+CommitmentSize:HeightSize+CommitmentSize+ShareOffsetSize], c.ShareOffset)
	binary.LittleEndian.PutUint32(id[HeightSize+CommitmentSize+ShareOffsetSize:FullIDSize], c.ShareSize)

	return id, nil
}

// UnmarshalBinary deserializes a byte slice into a CelestiaBlobID struct.
func (c *CelestiaBlobID) UnmarshalBinary(data []byte) error {
	if len(data) < FullIDSize {
		if len(data) < CompactIDSize {
			return fmt.Errorf("invalid ID length: expected at least %d bytes, got %d", CompactIDSize, len(data))
		}
		c.Height = binary.LittleEndian.Uint64(data[0:HeightSize])
		c.Commitment = make([]byte, CommitmentSize)
		copy(c.Commitment, data[HeightSize:CompactIDSize])
		return nil
	}

	c.Height = binary.LittleEndian.Uint64(data[0:HeightSize])
	c.Commitment = make([]byte, CommitmentSize)
	copy(c.Commitment, data[HeightSize:HeightSize+CommitmentSize])
	c.ShareOffset = binary.LittleEndian.Uint32(data[HeightSize+CommitmentSize : HeightSize+CommitmentSize+ShareOffsetSize])
	c.ShareSize = binary.LittleEndian.Uint32(data[HeightSize+CommitmentSize+ShareOffsetSize : FullIDSize])

	return nil
}

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
	SignerAddr    []byte // 20-byte signer address for BlobV1 (CIP-21)
}

// NewCelestiaStore returns a celestia store.
func NewCelestiaStore(ctx context.Context, cfg RPCClientConfig) (*CelestiaStore, error) {
	logger := log.New()
	var blobClient blobAPI.Module
	var signerAddr []byte
	var err error
	if cfg.TxClientConfig != nil {
		logger.Info("Initializing Celestia client in TxClient mode (OPTION B: service provider)",
			"rpc_url", cfg.URL,
			"grpc_addr", cfg.TxClientConfig.CoreGRPCAddr)
		blobClient, signerAddr, err = initTxClient(ctx, cfg)
	} else {
		logger.Info("Initializing Celestia client in RPC-only mode (OPTION A: self-hosted node)",
			"rpc_url", cfg.URL,
			"auth_token_set", cfg.AuthToken != "")
		blobClient, signerAddr, err = initRPCClient(ctx, cfg)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to initialize celestia client: %w", err)
	}
	namespace, err := libshare.NewNamespaceFromBytes(cfg.Namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to parse namespace: %w", err)
	}

	// Log signer address for verification
	if len(signerAddr) == 20 && !isZeroAddress(signerAddr) {
		signerBech32 := sdk.AccAddress(signerAddr).String()
		logger.Info("Celestia signer address configured",
			"signer_bech32", signerBech32,
			"signer_hex", hex.EncodeToString(signerAddr))
	}

	return &CelestiaStore{
		Log:           logger,
		Client:        blobClient,
		GetTimeout:    time.Minute,
		Namespace:     namespace,
		CompactBlobID: cfg.CompactBlobID,
		SignerAddr:    signerAddr,
	}, nil
}

// isZeroAddress checks if an address is all zeros
func isZeroAddress(addr []byte) bool {
	for _, b := range addr {
		if b != 0 {
			return false
		}
	}
	return true
}

// initTxClient initializes a transaction client for Celestia and extracts the signer address.
func initTxClient(ctx context.Context, cfg RPCClientConfig) (blobAPI.Module, []byte, error) {
	keyname := cfg.TxClientConfig.DefaultKeyName
	if keyname == "" {
		keyname = "my_celes_key"
	}
	kr, err := txClient.KeyringWithNewKey(txClient.KeyringConfig{
		KeyName:     keyname,
		BackendName: keyring.BackendTest,
	}, cfg.TxClientConfig.KeyringPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create keyring: %w", err)
	}

	// Extract signer address from keyring
	record, err := kr.Key(keyname)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get key from keyring: %w", err)
	}
	signerAccAddr, err := record.GetAddress()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get address from key: %w", err)
	}
	signerAddr := signerAccAddr.Bytes() // Convert to raw 20-byte address

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
	celestiaClient, err := txClient.New(ctx, config, kr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create tx client: %w", err)
	}
	return celestiaClient.Blob, signerAddr, nil
}

// initRPCClient initializes an RPC client for Celestia and extracts the signer address.
func initRPCClient(ctx context.Context, cfg RPCClientConfig) (blobAPI.Module, []byte, error) {
	logger := log.New()

	// Log auth token status with first/last 4 chars for verification
	tokenPreview := "not set"
	if cfg.AuthToken != "" {
		tokenLen := len(cfg.AuthToken)
		if tokenLen > 8 {
			tokenPreview = fmt.Sprintf("%s...%s (length: %d)",
				cfg.AuthToken[:4],
				cfg.AuthToken[tokenLen-4:],
				tokenLen)
		} else if tokenLen > 0 {
			tokenPreview = fmt.Sprintf("****** (length: %d)", tokenLen)
		}
	}

	logger.Info("Initializing RPC client for blob operations (Get, Subscribe, and Submit if not in read-only mode)",
		"url", cfg.URL,
		"auth_token", tokenPreview,
		"tls_enabled", cfg.TLSEnabled)

	celestiaClient, err := client.NewClient(ctx, cfg.URL, cfg.AuthToken)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create rpc client: %w", err)
	}

	logger.Info("RPC client initialized successfully - auth token will be used for all operations")

	// Extract signer address from the node's account
	// Even in RPC-only mode, the node has a keyring and can provide its signer address
	addr, err := celestiaClient.State.AccountAddress(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get account address from RPC node: %w", err)
	}

	// Convert state.Address to raw 20-byte address
	signerAddr := addr.Bytes()
	if len(signerAddr) != 20 {
		return nil, nil, fmt.Errorf("invalid signer address length from RPC node: expected 20 bytes, got %d", len(signerAddr))
	}

	return &celestiaClient.Blob, signerAddr, nil
}

func (d *CelestiaStore) Get(ctx context.Context, key []byte) ([]byte, error) {
	d.Log.Info("celestia: blob request", "id", hex.EncodeToString(key))
	// Use passed context instead of Background() to respect caller's cancellation
	ctx, cancel := context.WithTimeout(ctx, d.GetTimeout)
	defer cancel()

	// Validate key format: [alt-da_type_byte][celestia_version_byte][blob_id]
	//
	// The key follows Optimism's Alt-DA specification format:
	// - Byte 0: Commitment type byte (added by altda.GenericCommitment.Encode())
	//           Identifies the DA provider (Celestia, EigenDA, Avail, etc.)
	// - Byte 1: Celestia-specific version byte (VersionByte = 0x0c)
	//           Versions the Celestia blob ID format for forward compatibility
	// - Bytes 2+: Celestia blob ID (height + commitment + optional share info)
	//
	// This two-layer versioning enables:
	// 1. Multi-provider interoperability (Alt-DA type byte)
	// 2. Format evolution within Celestia (version byte)
	if len(key) < 2 {
		return nil, fmt.Errorf("invalid commitment format: expected at least 2 bytes [alt-da_type][celestia_version], got %d bytes. "+
			"Commitment must be obtained from Put() response or constructed via altda.NewGenericCommitment(append([]byte{VersionByte}, blobID...)).Encode()",
			len(key))
	}

	var blobID CelestiaBlobID
	// Skip first 2 bytes (alt-da type + celestia version) to get the blob ID
	if err := blobID.UnmarshalBinary(key[2:]); err != nil {
		return nil, fmt.Errorf("failed to unmarshal blob ID: %w", err)
	}
	log.Debug("Retrieving blob with commitment", "blobID.Commitment", hex.EncodeToString(blobID.Commitment), "blobID.Height", blobID.Height)
	blob, err := d.Client.Get(ctx, blobID.Height, d.Namespace, blobID.Commitment)
	if err != nil {
		return nil, fmt.Errorf("celestia: failed to resolve frame: %w", err)
	}
	if blob == nil {
		return nil, fmt.Errorf("celestia: failed to resolve frame: nil blob")
	}
	return blob.Data(), nil
}

func (d *CelestiaStore) Put(ctx context.Context, data []byte) ([]byte, []byte, error) {
	var submitFunc = func(ctx context.Context, client blobAPI.Module, b []*blob.Blob) (uint64, error) {
		return d.Client.Submit(ctx, b, state.NewTxConfig())
	}
	id, blobData, err := submitAndCreateBlobID(ctx, d.Client, submitFunc, d.Namespace, data, d.CompactBlobID, d.SignerAddr)
	if err != nil {
		return nil, nil, err
	}

	d.Log.Info("celestia: blob successfully submitted", "id", hex.EncodeToString(id))
	commitment := altda.NewGenericCommitment(append([]byte{VersionByte}, id...))
	return commitment.Encode(), blobData, nil
}

func (d *CelestiaStore) CreateCommitment(data []byte) ([]byte, error) {
	// Use BlobV1 for signed blobs (CIP-21)
	// BlobV1 requires 20-byte signer address
	// IMPORTANT: Use the same signer as Put() to ensure commitment matches
	if len(d.SignerAddr) != 20 {
		return nil, fmt.Errorf("invalid signer address: expected 20 bytes, got %d", len(d.SignerAddr))
	}
	b, err := blob.NewBlobV1(d.Namespace, data, d.SignerAddr)
	if err != nil {
		return nil, err
	}
	return b.Commitment, nil
}

// submitAndCreateBlobID submits a blob to Celestia and creates a marshaled blob ID.
// If compactBlobID is true, it re-fetches the blob to get its index and length.
func submitAndCreateBlobID(
	ctx context.Context,
	client blobAPI.Module,
	submitFunc func(context.Context, blobAPI.Module, []*blob.Blob) (uint64, error),
	namespace libshare.Namespace,
	data []byte,
	compactBlobID bool,
	signerAddr []byte,
) ([]byte, []byte, error) {
	// Use BlobV1 for signed blobs (CIP-21)
	// BlobV1 requires 20-byte signer address
	if len(signerAddr) != 20 {
		return nil, nil, fmt.Errorf("invalid signer address: expected 20 bytes, got %d", len(signerAddr))
	}
	b, err := blob.NewBlobV1(namespace, data, signerAddr)
	if err != nil {
		return nil, nil, err
	}

	height, err := submitFunc(ctx, client, []*blob.Blob{b})
	if err != nil {
		return nil, nil, err
	}

	var blobID CelestiaBlobID
	if compactBlobID {
		// Re-fetch the blob to get its index and length
		b, err = client.Get(ctx, height, namespace, b.Commitment)
		if err != nil {
			return nil, nil, err
		}

		size, err := b.Length()
		if err != nil {
			return nil, nil, err
		}

		blobID = CelestiaBlobID{
			Height:      height,
			Commitment:  b.Commitment,
			ShareOffset: uint32(b.Index()),
			ShareSize:   uint32(size),
			isCompact:   compactBlobID,
		}
	} else {
		blobID = CelestiaBlobID{
			Height:     height,
			Commitment: b.Commitment,
			isCompact:  compactBlobID,
		}
	}

	id, err := blobID.MarshalBinary()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal blob ID: %w", err)
	}

	return id, b.Data(), nil
}
