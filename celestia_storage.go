package celestia

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/celestiaorg/celestia-node/api/client"
	"github.com/celestiaorg/celestia-node/blob"
	"github.com/celestiaorg/celestia-node/nodebuilder/p2p"
	"github.com/celestiaorg/celestia-node/state"
	libshare "github.com/celestiaorg/go-square/v2/share"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	altda "github.com/ethereum-optimism/optimism/op-alt-da"
	"github.com/ethereum/go-ethereum/log"
)

// CelestiaBlobID represents the on-chain identifier for a Celestia blob.
type CelestiaBlobID struct {
	Height      uint64
	Commitment  []byte
	ShareOffset uint32
	ShareSize   uint32
}

// MarshalBinary serializes the CelestiaBlobID struct into a byte slice.
func (id *CelestiaBlobID) MarshalBinary() ([]byte, error) {
	// Calculate the total length of the marshaled ID
	// 8 bytes for Height + 32 bytes for Commitment + 4 bytes for ShareOffset + 4 bytes for ShareSize
	marshaledID := make([]byte, 8+32+4+4)

	binary.LittleEndian.PutUint64(marshaledID[0:8], id.Height)
	copy(marshaledID[8:40], id.Commitment) // Commitment is 32 bytes
	binary.LittleEndian.PutUint32(marshaledID[40:44], id.ShareOffset)
	binary.LittleEndian.PutUint32(marshaledID[44:48], id.ShareSize)

	return marshaledID, nil
}

// UnmarshalBinary deserializes a byte slice into a CelestiaBlobID struct.
func (id *CelestiaBlobID) UnmarshalBinary(data []byte) error {
	// Expected length: 8 bytes for Height + 32 bytes for Commitment + 4 bytes for ShareOffset + 4 bytes for ShareSize
	expectedLen := 8 + 32 + 4 + 4
	if len(data) < expectedLen {
		return fmt.Errorf("invalid ID length: expected at least %d bytes, got %d", expectedLen, len(data))
	}

	id.Height = binary.LittleEndian.Uint64(data[0:8])
	id.Commitment = make([]byte, 32)
	copy(id.Commitment, data[8:40]) // Commitment is 32 bytes
	id.ShareOffset = binary.LittleEndian.Uint32(data[40:44])
	id.ShareSize = binary.LittleEndian.Uint32(data[44:48])

	return nil
}

const VersionByte = 0x0c

type CelestiaConfig struct {
	URL                string
	TLSEnabled         bool
	AuthToken          string
	Namespace          []byte
	DefaultKeyName     string
	KeyringPath        string
	CoreGRPCAddr       string
	CoreGRPCTLSEnabled bool
	CoreGRPCAuthToken  string
	P2PNetwork         string
}

// CelestiaStore implements DAStorage with celestia backend
type CelestiaStore struct {
	Log        log.Logger
	GetTimeout time.Duration
	Namespace  libshare.Namespace
	Client     *client.Client
}

// NewCelestiaStore returns a celestia store.
func NewCelestiaStore(cfg CelestiaConfig) *CelestiaStore {
	keyname := cfg.DefaultKeyName
	if keyname == "" {
		keyname = "my_celes_key"
	}
	kr, err := client.KeyringWithNewKey(client.KeyringConfig{
		KeyName:     keyname,
		BackendName: keyring.BackendTest,
	}, cfg.KeyringPath)
	if err != nil {
		log.Crit("failed to create keyring", "err", err)
	}

	// Configure client
	config := client.Config{
		ReadConfig: client.ReadConfig{
			BridgeDAAddr: cfg.URL,
			DAAuthToken:  cfg.AuthToken,
			EnableDATLS:  cfg.TLSEnabled,
		},
		SubmitConfig: client.SubmitConfig{
			DefaultKeyName: cfg.DefaultKeyName,
			Network:        p2p.Network(cfg.P2PNetwork),
			CoreGRPCConfig: client.CoreGRPCConfig{
				Addr:       cfg.CoreGRPCAddr,
				TLSEnabled: cfg.CoreGRPCTLSEnabled,
				AuthToken:  cfg.CoreGRPCAuthToken,
			},
		},
	}

	ctx := context.Background()
	client, err := client.New(ctx, config, kr)
	if err != nil {
		log.Crit("failed to create celestia client", "err", err)
	}
	namespace, err := libshare.NewNamespaceFromBytes(cfg.Namespace)
	if err != nil {
		log.Crit("failed to parse namespace", "err", err)
	}
	return &CelestiaStore{
		Log:        log.New(),
		Client:     client,
		GetTimeout: time.Minute,
		Namespace:  namespace,
	}
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

	blob, err := d.Client.Blob.Get(ctx, blobID.Height, d.Namespace, blobID.Commitment)
	if err != nil || blob == nil {
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

	height, err := d.Client.Blob.Submit(ctx, []*blob.Blob{b}, state.NewTxConfig())
	if err != nil {
		return nil, err
	}

	// Re-fetch the blob to get its index and length
	b, err = d.Client.Blob.Get(ctx, height, d.Namespace, b.Commitment)
	if err != nil {
		return nil, err
	}

	size, err := b.Length()
	if err != nil {
		return nil, err
	}

	blobID := CelestiaBlobID{
		Height:      height,
		Commitment:  b.Commitment,
		ShareOffset: uint32(b.Index()),
		ShareSize:   uint32(size),
	}

	marshaledID, err := blobID.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal blob ID: %w", err)
	}

	d.Log.Info("celestia: blob successfully submitted", "id", hex.EncodeToString(marshaledID))
	commitment := altda.NewGenericCommitment(append([]byte{VersionByte}, marshaledID...))
	return commitment.Encode(), nil
}

func (d *CelestiaStore) CreateCommitment(data []byte) ([]byte, error) {
	b, err := blob.NewBlob(libshare.ShareVersionZero, d.Namespace, data, nil)
	if err != nil {
		return nil, err
	}
	return b.Commitment, nil
}
