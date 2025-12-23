package signer

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/ethereum/go-ethereum/log"
)

// NewKeyring creates a keyring.Keyring based on the provided configuration.
// Returns the keyring and the key name to use for signing.
func NewKeyring(cfg Config) (keyring.Keyring, string, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = ModeLocal
	}

	switch mode {
	case ModeLocal:
		log.Info("Creating local keyring",
			"keyring_path", cfg.Local.KeyringPath,
			"key_name", cfg.Local.KeyName)
		return NewLocalKeyring(cfg.Local)

	case ModePOPSigner:
		log.Info("Creating POPSigner keyring",
			"key_id", cfg.POPSigner.KeyID,
			"base_url", cfg.POPSigner.BaseURL)
		return NewPOPSignerKeyring(cfg.POPSigner)

	case ModeAWSKMS:
		return nil, "", fmt.Errorf("AWS KMS signer is not yet implemented")

	case ModeGCPKMS:
		return nil, "", fmt.Errorf("GCP Cloud KMS signer is not yet implemented")

	case ModeVault:
		return nil, "", fmt.Errorf("HashiCorp Vault signer is not yet implemented")

	default:
		return nil, "", fmt.Errorf("unknown signer mode: %s (valid: %s, %s, %s, %s, %s)",
			mode, ModeLocal, ModePOPSigner, ModeAWSKMS, ModeGCPKMS, ModeVault)
	}
}
