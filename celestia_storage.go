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

const VersionByte = 0x0c

// heightLen is a length (in bytes) of serialized height.
//
// This is 8 as uint64 consist of 8 bytes.
const heightLen = 8

func MakeID(height uint64, commitment []byte) []byte {
	id := make([]byte, heightLen+len(commitment))
	binary.LittleEndian.PutUint64(id, height)
	copy(id[heightLen:], commitment)
	return id
}

func SplitID(id []byte) (uint64, []byte) {
	if len(id) <= heightLen {
		return 0, nil
	}
	commitment := id[heightLen:]
	return binary.LittleEndian.Uint64(id[:heightLen]), commitment
}

type CelestiaConfig struct {
	DAAddr             string
	DATLSEnabled       bool
	DAAuthToken        string
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
			BridgeDAAddr: cfg.DAAddr,
			DAAuthToken:  cfg.DAAuthToken,
			EnableDATLS:  cfg.DATLSEnabled,
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
	Log := log.New()
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
		Log:        Log,
		Client:     client,
		GetTimeout: time.Minute,
		Namespace:  namespace,
	}
}

func (d *CelestiaStore) Get(ctx context.Context, key []byte) ([]byte, error) {
	log.Info("celestia: blob request", "id", hex.EncodeToString(key))
	ctx, cancel := context.WithTimeout(context.Background(), d.GetTimeout)
	height, commitment := SplitID(key[2:])
	blob, err := d.Client.Blob.Get(ctx, height, d.Namespace, commitment)
	cancel()
	if err != nil || blob == nil {
		return nil, fmt.Errorf("celestia: failed to resolve frame: %w", err)
	}
	return blob.Data(), nil
}

func (d *CelestiaStore) Put(ctx context.Context, data []byte) ([]byte, error) {
	b, err := blob.NewBlob(libshare.ShareVersionZero, d.Namespace, data, nil)
	if err != nil {
		fmt.Println("failed to create blob:", err)
		return nil, err
	}
	height, err := d.Client.Blob.Submit(ctx, []*blob.Blob{b}, state.NewTxConfig())
	if err == nil {
		id := MakeID(height, b.Commitment)
		d.Log.Info("celestia: blob successfully submitted", "id", hex.EncodeToString(id))
		commitment := altda.NewGenericCommitment(append([]byte{VersionByte}, id...))
		return commitment.Encode(), nil
	}
	return nil, err
}

func (d *CelestiaStore) CreateCommitment(data []byte) ([]byte, error) {
	b, err := blob.NewBlob(libshare.ShareVersionZero, d.Namespace, data, nil)
	if err != nil {
		return nil, err
	}
	return b.Commitment, nil
}
