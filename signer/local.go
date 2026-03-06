package signer

import (
	"fmt"

	txClient "github.com/celestiaorg/celestia-node/api/client"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
)

// NewLocalKeyring creates a local filesystem keyring.
// Returns the keyring and the key name to use.
func NewLocalKeyring(cfg LocalConfig) (keyring.Keyring, string, error) {
	if cfg.KeyringPath == "" {
		return nil, "", fmt.Errorf("local keyring_path is required")
	}
	if cfg.KeyName == "" {
		return nil, "", fmt.Errorf("local key_name is required")
	}
	backend := cfg.BackendName
	if backend == "" {
		backend = keyring.BackendTest
	}

	// Create the keyring using celestia-node's helper
	kr, err := txClient.KeyringWithNewKey(txClient.KeyringConfig{
		KeyName:     cfg.KeyName,
		BackendName: backend,
	}, cfg.KeyringPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create local keyring: %w", err)
	}

	return kr, cfg.KeyName, nil
}
