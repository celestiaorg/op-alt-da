package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/op-alt-da/sdkconfig"
)

func init() {
	// Configure SDK to use Celestia Bech32 prefix
	sdkconfig.InitCelestiaPrefix()
}

func TestValidateTrustedSigners(t *testing.T) {
	tests := []struct {
		name        string
		input       []string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid Bech32 address",
			input:       []string{"celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc"},
			expectError: false,
		},
		{
			name:        "multiple valid Bech32 addresses",
			input:       []string{"celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc", "celestia1qqgjyv6y24n80zye42aueh0wluqsyqcyf07sls"},
			expectError: false,
		},
		{
			name:        "invalid Bech32 address",
			input:       []string{"celestia1invalid"},
			expectError: true,
			errorMsg:    "invalid Bech32 address",
		},
		{
			name:        "hex address not allowed",
			input:       []string{"a6fd02b5ff6b4bc19768b977a1d6d88974d0e8f0"},
			expectError: true,
			errorMsg:    "must be a Celestia Bech32 address",
		},
		{
			name:        "non-celestia prefix",
			input:       []string{"cosmos15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc"},
			expectError: true,
			errorMsg:    "must be a Celestia Bech32 address",
		},
		{
			name:        "empty input",
			input:       []string{},
			expectError: false,
		},
		{
			name:        "whitespace trimmed",
			input:       []string{"  celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc  "},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTrustedSigners(tt.input)

			if tt.expectError {
				require.Error(t, err, "expected error but got none")
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg, "error message should contain expected text")
				}
			} else {
				require.NoError(t, err, "unexpected error")
			}
		})
	}
}

func TestConfig_Validate_TrustedSigners(t *testing.T) {
	tests := []struct {
		name           string
		trustedSigners []string
		expectError    bool
		errorMsg       string
	}{
		{
			name:           "valid Bech32 signer",
			trustedSigners: []string{"celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc"},
			expectError:    false,
		},
		{
			name:           "multiple valid Bech32 signers",
			trustedSigners: []string{"celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc", "celestia1qqgjyv6y24n80zye42aueh0wluqsyqcyf07sls"},
			expectError:    false,
		},
		{
			name:           "no signers (allowed in single-server mode)",
			trustedSigners: []string{},
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Addr:   "localhost",
				Port:   8080,
				DBPath: "/tmp/test.db",
				Celestia: CelestiaConfig{
					Namespace:      "000000000000000000000000000000000000000102030405060708090a",
					BridgeAddr:     "localhost:26658",
					CoreGRPCAddr:   "localhost:9090",
					KeyringPath:    "/tmp/test-keyring",
					P2PNetwork:     "mocha-4",
					DefaultKeyName: "test_key",
				},
				Batch: BatchConfig{
					MinBlobs:    1,
					MaxBlobs:    100,
					TargetBlobs: 50,
					MaxSizeKB:   10240,
					MinSizeKB:   1,
				},
				Worker: WorkerConfig{
					SubmitTimeout:   "1m",
					MaxRetries:      3,
					ReconcilePeriod: "1m",
					ReconcileAge:    "2m",
					GetTimeout:      "30s",
					TrustedSigners:  tt.trustedSigners,
				},
			}

			err := cfg.Validate()

			if tt.expectError {
				require.Error(t, err, "expected validation error")
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg, "error message should contain expected text")
				}
			} else {
				require.NoError(t, err, "unexpected validation error")
				// Signers should remain in Bech32 format
				if len(tt.trustedSigners) > 0 {
					for _, signer := range cfg.Worker.TrustedSigners {
						assert.True(t, strings.HasPrefix(signer, "celestia"), "signer should remain in Bech32 format with celestia prefix")
					}
				}
			}
		})
	}
}

func TestConfig_BuildFromTOML(t *testing.T) {
	cfg := &Config{
		Addr:   "localhost",
		Port:   3100,
		DBPath: "/tmp/test.db",
		Celestia: CelestiaConfig{
			Namespace:      "000000000000000000000000000000000000000102030405060708090a",
			BridgeAddr:     "localhost:26658",
			CoreGRPCAddr:   "localhost:9090",
			KeyringPath:    "/tmp/test-keyring",
			P2PNetwork:     "mocha-4",
			DefaultKeyName: "test_key",
		},
		Batch: BatchConfig{
			MinBlobs:    1,
			MaxBlobs:    100,
			TargetBlobs: 50,
			MaxSizeKB:   1024,
			MinSizeKB:   1,
		},
	Worker: WorkerConfig{
		SubmitTimeout:   "1m",
		MaxRetries:      3,
		ReconcilePeriod: "1m",
		ReconcileAge:    "2m",
		GetTimeout:      "30s",
		TrustedSigners:  []string{"celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc"},
	},
		Backfill: BackfillConfig{
			Enabled:       true,
			StartHeight:   1000,
			TargetHeight:  2000,
			BlocksPerScan: 50,
			Period:        "1s",
		},
	}

	runtimeCfg, err := BuildConfigFromTOML(cfg)
	require.NoError(t, err, "Failed to build runtime config from TOML")

	// Check trusted_signers is set correctly
	assert.NotEmpty(t, runtimeCfg.WorkerConfig.TrustedSigners, "Expected trusted_signers to be non-empty")
	assert.Equal(t, "celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc", runtimeCfg.WorkerConfig.TrustedSigners[0])

	// Check backfill config
	assert.True(t, runtimeCfg.WorkerConfig.BackfillEnabled)
	assert.Equal(t, uint64(1000), runtimeCfg.WorkerConfig.StartHeight)
	assert.Equal(t, uint64(2000), runtimeCfg.WorkerConfig.BackfillTargetHeight)
	assert.Equal(t, 50, runtimeCfg.WorkerConfig.BlocksPerScan)

	t.Logf("✓ Config built correctly from TOML")
	t.Logf("  TrustedSigners: %v", runtimeCfg.WorkerConfig.TrustedSigners)
	t.Logf("  BackfillEnabled: %v", runtimeCfg.WorkerConfig.BackfillEnabled)
	t.Logf("  BackfillTargetHeight: %d", runtimeCfg.WorkerConfig.BackfillTargetHeight)
}
