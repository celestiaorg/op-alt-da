package signer

import (
	"context"
	"fmt"

	awskeyring "github.com/celestiaorg/aws-kms-keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
)

// NewAWSKMSKeyring creates an AWS KMS-backed keyring and returns the key alias used for signing.
func NewAWSKMSKeyring(cfg AWSKMSConfig, keyName string) (keyring.Keyring, string, error) {
	if cfg.Region == "" {
		return nil, "", fmt.Errorf("aws_kms region is required")
	}
	if keyName == "" {
		keyName = cfg.KeyID
	}
	if keyName == "" {
		return nil, "", fmt.Errorf("aws_kms key_id is required")
	}

	kr, err := awskeyring.NewKMSKeyring(context.Background(), awskeyring.Config{
		Region:   cfg.Region,
		Endpoint: cfg.Endpoint,
		KeyName:  keyName,
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to create aws kms keyring: %w", err)
	}

	return kr, keyName, nil
}
