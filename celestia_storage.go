package celestia

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	txClient "github.com/celestiaorg/celestia-node/api/client"
	blobAPI "github.com/celestiaorg/celestia-node/nodebuilder/blob"
	"github.com/celestiaorg/celestia-node/nodebuilder/p2p"
	libshare "github.com/celestiaorg/go-square/v3/share"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
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
	// VersionByte identifies Celestia blob format in Alt-DA commitments.
	// Commitment format: [alt-da_type][version_byte][blob_id]
	// The two-layer versioning enables multi-provider interoperability and format evolution.
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

// CelestiaClientConfig holds configuration for connecting to Celestia.
// Uses a dual-endpoint architecture:
//   - Bridge node (JSON-RPC) for reading blobs
//   - CoreGRPC for submitting blobs to consensus (not needed in read-only mode)
type CelestiaClientConfig struct {
	// Read-only mode: only reads blobs from bridge node, doesn't submit
	ReadOnly bool

	// Bridge node settings (for reading blobs via JSON-RPC)
	BridgeAddr       string // Bridge node JSON-RPC endpoint (e.g., http://localhost:26658)
	BridgeAuthToken  string // Auth token for bridge node (optional, some providers require it)
	BridgeTLSEnabled bool   // Enable TLS for bridge node connection

	// CoreGRPC settings (for submitting blobs) - not needed in read-only mode
	CoreGRPCAddr       string // Consensus node gRPC endpoint (e.g., consensus-full-mocha-4.celestia-mocha.com:9090)
	CoreGRPCAuthToken  string // Auth token for gRPC (optional, some providers like QuickNode require it)
	CoreGRPCTLSEnabled bool   // Enable TLS for gRPC connection

	// Keyring settings (for signing transactions)
	KeyringPath    string // Path to keyring directory (e.g., ~/.celestia-light-mocha-4/keys)
	DefaultKeyName string // Key name to use for signing (e.g., "my_celes_key")
	P2PNetwork     string // Celestia network (e.g., mocha-4, arabica-11, mainnet)

	// Parallel submission settings
	// TxWorkerAccounts controls parallel transaction submission:
	//   - 0: Immediate submission (no queue, default)
	//   - 1: Synchronous submission (queued, single signer)
	//   - >1: Parallel submission (queued, multiple worker accounts)
	// When >1, ordering is NOT guaranteed. Worker addresses must be added to reader's trusted_signers.
	TxWorkerAccounts int

	// Blob settings
	Namespace     []byte // Celestia namespace for blobs
	CompactBlobID bool   // Use compact blob IDs (recommended)
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
// Architecture:
//   - Reading blobs: Uses bridge node JSON-RPC (BridgeAddr + optional BridgeAuthToken)
//   - Writing blobs: Uses CoreGRPC for direct submission to consensus (not in read-only mode)
func NewCelestiaStore(ctx context.Context, cfg CelestiaClientConfig) (*CelestiaStore, error) {
	logger := log.New()

	namespace, err := libshare.NewNamespaceFromBytes(cfg.Namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to parse namespace: %w", err)
	}

	// Read-only mode: only initialize read client (no keyring or CoreGRPC needed)
	if cfg.ReadOnly {
		logger.Info("Initializing Celestia client (READ-ONLY mode)",
			"bridge_addr", cfg.BridgeAddr,
			"bridge_auth_token_set", cfg.BridgeAuthToken != "")

		readClient, err := initReadOnlyClient(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize read-only celestia client: %w", err)
		}

		return &CelestiaStore{
			Log:           logger,
			Client:        readClient,
			GetTimeout:    time.Minute,
			Namespace:     namespace,
			CompactBlobID: cfg.CompactBlobID,
			SignerAddr:    nil, // No signer in read-only mode
		}, nil
	}

	// Write mode: validate required fields
	if cfg.CoreGRPCAddr == "" {
		return nil, fmt.Errorf("CoreGRPCAddr is required for blob submission")
	}
	if cfg.KeyringPath == "" {
		return nil, fmt.Errorf("KeyringPath is required for signing transactions")
	}

	logger.Info("Initializing Celestia client (WRITE mode)",
		"bridge_addr", cfg.BridgeAddr,
		"grpc_addr", cfg.CoreGRPCAddr,
		"bridge_auth_token_set", cfg.BridgeAuthToken != "",
		"grpc_auth_token_set", cfg.CoreGRPCAuthToken != "")

	blobClient, signerAddr, err := initCelestiaClient(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize celestia client: %w", err)
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

// SignerAddresses represents all signer addresses that will be used for blob submission.
// When TxWorkerAccounts > 1 is configured on the Celestia node, it creates additional worker accounts.
// All these addresses must be added to the reader's trusted_signers for security.
type SignerAddresses struct {
	Primary string   // Primary signer address (from DefaultKeyName)
	Workers []string // Worker signer addresses (created by Celestia node when TxWorkerAccounts > 1)
}

// InitializeSignerAddresses initializes the keyring and returns all signer addresses.
// This should be called before starting the server to get the addresses needed for reader's trusted_signers.
//
// For parallel submission (TxWorkerAccounts > 1), worker accounts are created by the Celestia client
// when the server starts. This function creates/lists the worker keys to show their addresses.
//
// Worker keys follow the celestia-app convention: parallel-worker-{i} (e.g., parallel-worker-1)
func InitializeSignerAddresses(cfg CelestiaClientConfig) (*SignerAddresses, error) {
	keyname := cfg.DefaultKeyName
	if keyname == "" {
		keyname = "my_celes_key"
	}

	// Initialize keyring with the primary key
	kr, err := txClient.KeyringWithNewKey(txClient.KeyringConfig{
		KeyName:     keyname,
		BackendName: keyring.BackendTest,
	}, cfg.KeyringPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create keyring: %w", err)
	}

	// Get primary signer address
	record, err := kr.Key(keyname)
	if err != nil {
		return nil, fmt.Errorf("failed to get key from keyring: %w", err)
	}
	primaryAddr, err := record.GetAddress()
	if err != nil {
		return nil, fmt.Errorf("failed to get address from key: %w", err)
	}

	addresses := &SignerAddresses{
		Primary: primaryAddr.String(),
		Workers: make([]string, 0),
	}

	// Generate worker addresses based on TxWorkerAccounts config
	// Worker accounts follow celestia-app convention: parallel-worker-{i}
	// Worker index starts at 1 (index 0 is the primary account)
	if cfg.TxWorkerAccounts > 1 {
		for i := 1; i < cfg.TxWorkerAccounts; i++ {
			workerName := fmt.Sprintf("parallel-worker-%d", i)

			// Try to get existing worker key, or create it
			workerKey, err := kr.Key(workerName)
			if err != nil {
				// Key doesn't exist, create it
				workerKey, _, err = kr.NewMnemonic(
					workerName,
					keyring.English,
					sdk.GetConfig().GetFullBIP44Path(),
					keyring.DefaultBIP39Passphrase,
					hd.Secp256k1,
				)
				if err != nil {
					return nil, fmt.Errorf("failed to create worker key %s: %w", workerName, err)
				}
			}

			workerAddr, err := workerKey.GetAddress()
			if err != nil {
				return nil, fmt.Errorf("failed to get address for worker %s: %w", workerName, err)
			}
			addresses.Workers = append(addresses.Workers, workerAddr.String())
		}
	}

	return addresses, nil
}

// AllAddresses returns all signer addresses (primary + workers) as a slice.
// This is useful for generating the trusted_signers list for readers.
func (s *SignerAddresses) AllAddresses() []string {
	all := make([]string, 0, 1+len(s.Workers))
	all = append(all, s.Primary)
	all = append(all, s.Workers...)
	return all
}

// initReadOnlyClient initializes a read-only Celestia client for blob retrieval.
// Uses only the bridge node (no keyring or CoreGRPC needed).
func initReadOnlyClient(ctx context.Context, cfg CelestiaClientConfig) (blobAPI.Module, error) {
	config := txClient.Config{
		ReadConfig: txClient.ReadConfig{
			BridgeDAAddr: cfg.BridgeAddr,
			DAAuthToken:  cfg.BridgeAuthToken,
			EnableDATLS:  cfg.BridgeTLSEnabled,
		},
		// No SubmitConfig needed for read-only mode
	}

	client, err := txClient.New(ctx, config, nil) // No keyring needed
	if err != nil {
		return nil, fmt.Errorf("failed to create read-only celestia client: %w", err)
	}

	return client.Blob, nil
}

// initCelestiaClient initializes a Celestia client and extracts the signer address.
// Uses dual-endpoint architecture:
//   - ReadConfig: Bridge node JSON-RPC for reading blobs
//   - SubmitConfig: CoreGRPC for submitting blobs to consensus
func initCelestiaClient(ctx context.Context, cfg CelestiaClientConfig) (blobAPI.Module, []byte, error) {
	keyname := cfg.DefaultKeyName
	if keyname == "" {
		keyname = "my_celes_key"
	}
	kr, err := txClient.KeyringWithNewKey(txClient.KeyringConfig{
		KeyName:     keyname,
		BackendName: keyring.BackendTest,
	}, cfg.KeyringPath)
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

	// Configure client with dual-endpoint architecture
	// TxWorkerAccounts controls parallel transaction submission:
	//   - 0: Immediate submission (no queue, default)
	//   - 1: Synchronous submission (queued, single signer)
	//   - >1: Parallel submission (queued, multiple worker accounts)
	// When >1, the client creates worker subaccounts for parallel TX submission.
	// These subaccount addresses must be added to readers' trusted_signers.
	config := txClient.Config{
		ReadConfig: txClient.ReadConfig{
			BridgeDAAddr: cfg.BridgeAddr,
			DAAuthToken:  cfg.BridgeAuthToken,
			EnableDATLS:  cfg.BridgeTLSEnabled,
		},
		SubmitConfig: txClient.SubmitConfig{
			DefaultKeyName:   cfg.DefaultKeyName,
			Network:          p2p.Network(cfg.P2PNetwork),
			TxWorkerAccounts: cfg.TxWorkerAccounts,
			CoreGRPCConfig: txClient.CoreGRPCConfig{
				Addr:       cfg.CoreGRPCAddr,
				TLSEnabled: cfg.CoreGRPCTLSEnabled,
				AuthToken:  cfg.CoreGRPCAuthToken,
			},
		},
	}

	// Log submission mode
	switch {
	case cfg.TxWorkerAccounts > 1:
		log.Info("Parallel submission mode enabled",
			"tx_worker_accounts", cfg.TxWorkerAccounts,
			"note", "Worker addresses must be in reader's trusted_signers")
	case cfg.TxWorkerAccounts == 1:
		log.Info("Synchronous submission mode enabled (queued, single signer)")
	default:
		log.Info("Immediate submission mode (default, no queue)")
	}

	celestiaClient, err := txClient.New(ctx, config, kr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create celestia client: %w", err)
	}
	return celestiaClient.Blob, signerAddr, nil
}
