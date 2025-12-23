package signer

import (
	"fmt"

	popsigner "github.com/Bidon15/popsigner/sdk-go"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
)

// NewPOPSignerKeyring creates a POPSigner-backed keyring.
// Returns the keyring and the key name to use.
func NewPOPSignerKeyring(cfg POPSignerConfig) (keyring.Keyring, string, error) {
	apiKey := cfg.ResolveAPIKey()
	if apiKey == "" {
		return nil, "", fmt.Errorf("popsigner api_key is required (set in config or via POPSIGNER_API_KEY env var)")
	}
	if cfg.KeyID == "" {
		return nil, "", fmt.Errorf("popsigner key_id is required")
	}

	opts := []popsigner.CelestiaKeyringOption{}
	if cfg.BaseURL != "" {
		opts = append(opts, popsigner.WithCelestiaBaseURL(cfg.BaseURL))
	}

	kr, err := popsigner.NewCelestiaKeyring(apiKey, cfg.KeyID, opts...)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create popsigner keyring: %w", err)
	}

	return kr, kr.KeyName(), nil
}
