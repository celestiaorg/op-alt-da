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
		readOnly       bool
		trustedSigners []string
		expectError    bool
		errorMsg       string
	}{
		{
			name:           "read-only with valid Bech32 signer",
			readOnly:       true,
			trustedSigners: []string{"celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc"},
			expectError:    false,
		},
		{
			name:           "read-only with valid second Bech32 signer",
			readOnly:       true,
			trustedSigners: []string{"celestia1qqgjyv6y24n80zye42aueh0wluqsyqcyf07sls"},
			expectError:    false,
		},
		{
			name:           "read-only with no signers",
			readOnly:       true,
			trustedSigners: []string{},
			expectError:    true,
			errorMsg:       "read-only mode requires worker.trusted_signers",
		},
		{
			name:           "read-only with invalid signer",
			readOnly:       true,
			trustedSigners: []string{"invalid"},
			expectError:    true,
			errorMsg:       "invalid worker.trusted_signers",
		},
		{
			name:           "not read-only with no signers",
			readOnly:       false,
			trustedSigners: []string{},
			expectError:    false, // No validation when not read-only
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Addr:   "localhost",
				Port:   8080,
				DBPath: "/tmp/test.db",
				Celestia: CelestiaConfig{
					Namespace:   "000000000000000000000000000000000000000102030405060708090a",
					DARPCServer: "localhost:26658",
					AuthToken:   "test-token",
				},
				Batch: BatchConfig{
					MinBlobs:    1,
					MaxBlobs:    100,
					TargetBlobs: 50,
					MaxSizeMB:   10,
					MinSizeKB:   1,
				},
				Worker: WorkerConfig{
					SubmitPeriod:     "30s",
					SubmitTimeout:    "1m",
					MaxRetries:       3,
					MaxBlobWaitTime:  "5m",
					ReconcilePeriod:  "1m",
					ReconcileAge:     "2m",
					GetTimeout:       "30s",
					TrustedSigners:   tt.trustedSigners,
				},
				ReadOnly: tt.readOnly,
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
				if tt.readOnly && len(tt.trustedSigners) > 0 {
					for _, signer := range cfg.Worker.TrustedSigners {
						assert.True(t, strings.HasPrefix(signer, "celestia"), "signer should remain in Bech32 format with celestia prefix")
					}
				}
			}
		})
	}
}
