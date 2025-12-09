package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/urfave/cli/v2"
)

const (
	// ConfigFileFlagName is the flag name for the config file path.
	ConfigFileFlagName = "config"
)

var (
	// ConfigFileFlag is the CLI flag for specifying a TOML config file.
	ConfigFileFlag = &cli.StringFlag{
		Name:    ConfigFileFlagName,
		Usage:   "Path to TOML configuration file",
		EnvVars: prefixEnvVars("CONFIG"),
	}
)

// LoadConfigFromFile loads configuration from a TOML file.
func LoadConfigFromFile(path string) (*Config, error) {
	// Expand ~ to home directory
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		path = filepath.Join(home, path[1:])
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := DefaultConfig()
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Expand ~ in keyring path
	if strings.HasPrefix(cfg.Celestia.KeyringPath, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		cfg.Celestia.KeyringPath = filepath.Join(home, cfg.Celestia.KeyringPath[1:])
	}

	return &cfg, nil
}

// BuildConfigFromCLI builds a Config from CLI flags.
// If a config file is specified, it loads from that file first,
// then CLI flags override the file values.
func BuildConfigFromCLI(ctx *cli.Context) (*Config, error) {
	var cfg *Config

	// Load from file if specified
	if configPath := ctx.String(ConfigFileFlagName); configPath != "" {
		var err error
		cfg, err = LoadConfigFromFile(configPath)
		if err != nil {
			return nil, err
		}
	} else {
		// Start with defaults
		defaultCfg := DefaultConfig()
		cfg = &defaultCfg
	}

	// Override with CLI flags if they were explicitly set
	if ctx.IsSet(ListenAddrFlagName) {
		cfg.Addr = ctx.String(ListenAddrFlagName)
	}
	if ctx.IsSet(PortFlagName) {
		cfg.Port = ctx.Int(PortFlagName)
	}

	// Celestia flags
	if ctx.IsSet(CelestiaNamespaceFlagName) {
		cfg.Celestia.Namespace = ctx.String(CelestiaNamespaceFlagName)
	}
	if ctx.IsSet(CelestiaServerFlagName) {
		cfg.Celestia.BridgeAddr = ctx.String(CelestiaServerFlagName)
	}
	if ctx.IsSet(CelestiaTLSEnabledFlagName) {
		cfg.Celestia.BridgeTLSEnabled = ctx.Bool(CelestiaTLSEnabledFlagName)
	}
	if ctx.IsSet(CelestiaAuthTokenFlagName) {
		cfg.Celestia.BridgeAuthToken = ctx.String(CelestiaAuthTokenFlagName)
	}
	if ctx.IsSet(CelestiaCompactBlobIDFlagName) {
		cfg.Celestia.BlobIDCompact = ctx.Bool(CelestiaCompactBlobIDFlagName)
	}

	// TX client flags
	if ctx.IsSet(CelestiaDefaultKeyNameFlagName) {
		cfg.Celestia.DefaultKeyName = ctx.String(CelestiaDefaultKeyNameFlagName)
	}
	if ctx.IsSet(CelestiaKeyringPathFlagName) {
		cfg.Celestia.KeyringPath = ctx.String(CelestiaKeyringPathFlagName)
	}
	if ctx.IsSet(CelestiaCoreGRPCAddrFlagName) {
		cfg.Celestia.CoreGRPCAddr = ctx.String(CelestiaCoreGRPCAddrFlagName)
	}
	if ctx.IsSet(CelestiaCoreGRPCTLSEnabledFlagName) {
		cfg.Celestia.CoreGRPCTLSEnabled = ctx.Bool(CelestiaCoreGRPCTLSEnabledFlagName)
	}
	if ctx.IsSet(CelestiaCoreGRPCAuthTokenFlagName) {
		cfg.Celestia.CoreGRPCAuthToken = ctx.String(CelestiaCoreGRPCAuthTokenFlagName)
	}
	if ctx.IsSet(CelestiaP2PNetworkFlagName) {
		cfg.Celestia.P2PNetwork = ctx.String(CelestiaP2PNetworkFlagName)
	}

	// Metrics flags
	if ctx.IsSet(MetricsEnabledFlagName) {
		cfg.Metrics.Enabled = ctx.Bool(MetricsEnabledFlagName)
	}
	if ctx.IsSet(MetricsPortFlagName) {
		cfg.Metrics.Port = ctx.Int(MetricsPortFlagName)
	}

	// Fallback flags
	if ctx.IsSet(FallbackEnabledFlagName) {
		cfg.Fallback.Enabled = ctx.Bool(FallbackEnabledFlagName)
	}
	if ctx.IsSet(FallbackProviderFlagName) {
		cfg.Fallback.Provider = ctx.String(FallbackProviderFlagName)
	}
	if ctx.IsSet(FallbackS3BucketFlagName) {
		cfg.Fallback.S3.Bucket = ctx.String(FallbackS3BucketFlagName)
	}
	if ctx.IsSet(FallbackS3PrefixFlagName) {
		cfg.Fallback.S3.Prefix = ctx.String(FallbackS3PrefixFlagName)
	}
	if ctx.IsSet(FallbackS3EndpointFlagName) {
		cfg.Fallback.S3.Endpoint = ctx.String(FallbackS3EndpointFlagName)
	}
	if ctx.IsSet(FallbackS3RegionFlagName) {
		cfg.Fallback.S3.Region = ctx.String(FallbackS3RegionFlagName)
	}
	if ctx.IsSet(FallbackS3CredTypeFlagName) {
		cfg.Fallback.S3.CredentialType = ctx.String(FallbackS3CredTypeFlagName)
	}
	if ctx.IsSet(FallbackS3AccessKeyFlagName) {
		cfg.Fallback.S3.AccessKeyID = ctx.String(FallbackS3AccessKeyFlagName)
	}
	if ctx.IsSet(FallbackS3SecretKeyFlagName) {
		cfg.Fallback.S3.AccessKeySecret = ctx.String(FallbackS3SecretKeyFlagName)
	}
	if ctx.IsSet(FallbackS3TimeoutFlagName) {
		cfg.Fallback.S3.Timeout = ctx.Duration(FallbackS3TimeoutFlagName).String()
	}

	return cfg, nil
}

