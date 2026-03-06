package main

import (
	"strings"
	"testing"

	"github.com/celestiaorg/op-alt-da/signer"
)

func TestBuildSignerConfig_LegacyAWSKMSBackend(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Celestia.KeyringBackend = "awskms"
	cfg.Celestia.DefaultKeyName = "alias/op-alt-da/signer"
	cfg.Celestia.AWSKMS.Region = "us-east-1"
	cfg.Celestia.AWSKMS.Endpoint = "http://localhost:4566"

	signerCfg := cfg.buildSignerConfig()

	if signerCfg.Mode != signer.ModeAWSKMS {
		t.Fatalf("expected mode %q, got %q", signer.ModeAWSKMS, signerCfg.Mode)
	}
	if signerCfg.AWSKMS.KeyID != cfg.Celestia.DefaultKeyName {
		t.Fatalf("expected aws key id %q, got %q", cfg.Celestia.DefaultKeyName, signerCfg.AWSKMS.KeyID)
	}
	if signerCfg.AWSKMS.Region != cfg.Celestia.AWSKMS.Region {
		t.Fatalf("expected aws region %q, got %q", cfg.Celestia.AWSKMS.Region, signerCfg.AWSKMS.Region)
	}
	if signerCfg.AWSKMS.Endpoint != cfg.Celestia.AWSKMS.Endpoint {
		t.Fatalf("expected aws endpoint %q, got %q", cfg.Celestia.AWSKMS.Endpoint, signerCfg.AWSKMS.Endpoint)
	}
	if err := signerCfg.Validate(); err != nil {
		t.Fatalf("expected legacy awskms signer config to validate, got error: %v", err)
	}
}

func TestBuildSignerConfig_LegacySignerModeAliasAWSKMS(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Celestia.SignerMode = "awskms"
	cfg.Celestia.DefaultKeyName = "alias/op-alt-da/signer"
	cfg.Celestia.AWSKMS.Region = "us-east-1"

	signerCfg := cfg.buildSignerConfig()

	if signerCfg.Mode != signer.ModeAWSKMS {
		t.Fatalf("expected mode %q for signer_mode=awskms, got %q", signer.ModeAWSKMS, signerCfg.Mode)
	}
	if err := signerCfg.Validate(); err != nil {
		t.Fatalf("expected signer_mode=awskms to validate, got error: %v", err)
	}
}

func TestValidate_LegacyAWSKMSConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Celestia.Namespace = strings.Repeat("0", 58)
	cfg.Celestia.CoreGRPCAddr = "localhost:9090"
	cfg.Celestia.P2PNetwork = "mocha-4"
	cfg.Celestia.KeyringBackend = "awskms"
	cfg.Celestia.DefaultKeyName = "alias/op-alt-da/signer"
	cfg.Celestia.AWSKMS.Region = "us-east-1"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected legacy awskms config to pass validation, got error: %v", err)
	}
}
