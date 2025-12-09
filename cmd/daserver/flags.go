package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/urfave/cli/v2"

	celestia "github.com/celestiaorg/op-alt-da"
	"github.com/celestiaorg/op-alt-da/fallback/s3"
	opservice "github.com/ethereum-optimism/optimism/op-service"
	oplog "github.com/ethereum-optimism/optimism/op-service/log"
)

const (
	ListenAddrFlagName         = "addr"
	PortFlagName               = "port"
	CelestiaServerFlagName     = "celestia.server"
	CelestiaTLSEnabledFlagName = "celestia.tls-enabled"
	CelestiaAuthTokenFlagName  = "celestia.auth-token"
	CelestiaNamespaceFlagName  = "celestia.namespace"

	// tx client config flags
	CelestiaDefaultKeyNameFlagName     = "celestia.tx-client.key-name"
	CelestiaKeyringPathFlagName        = "celestia.tx-client.keyring-path"
	CelestiaCoreGRPCAddrFlagName       = "celestia.tx-client.core-grpc.addr"
	CelestiaCoreGRPCTLSEnabledFlagName = "celestia.tx-client.core-grpc.tls-enabled"
	CelestiaCoreGRPCAuthTokenFlagName  = "celestia.tx-client.core-grpc.auth-token"
	CelestiaP2PNetworkFlagName         = "celestia.tx-client.p2p-network"

	CelestiaCompactBlobIDFlagName = "celestia.compact-blobid"

	// metrics
	MetricsEnabledFlagName = "metrics.enabled"
	MetricsPortFlagName    = "metrics.port"

	// fallback provider flags
	FallbackEnabledFlagName     = "fallback.enabled"
	FallbackProviderFlagName    = "fallback.provider"
	FallbackS3BucketFlagName    = "fallback.s3.bucket"
	FallbackS3PrefixFlagName    = "fallback.s3.prefix"
	FallbackS3EndpointFlagName  = "fallback.s3.endpoint"
	FallbackS3RegionFlagName    = "fallback.s3.region"
	FallbackS3CredTypeFlagName  = "fallback.s3.credential-type"
	FallbackS3AccessKeyFlagName = "fallback.s3.access-key-id"
	FallbackS3SecretKeyFlagName = "fallback.s3.access-key-secret"
	FallbackS3TimeoutFlagName   = "fallback.s3.timeout"
)

const EnvVarPrefix = "OP_ALTDA"

func prefixEnvVars(name string) []string {
	return opservice.PrefixEnvVar(EnvVarPrefix, name)
}

var (
	ListenAddrFlag = &cli.StringFlag{
		Name:    ListenAddrFlagName,
		Usage:   "alt da server listening address",
		Value:   "127.0.0.1",
		EnvVars: prefixEnvVars("ADDR"),
	}
	PortFlag = &cli.IntFlag{
		Name:    PortFlagName,
		Usage:   "alt da server listening port",
		Value:   3100,
		EnvVars: prefixEnvVars("PORT"),
	}
	CelestiaServerFlag = &cli.StringFlag{
		Name:    CelestiaServerFlagName,
		Usage:   "celestia rpc endpoint (bridge node for reads)",
		Value:   "http://localhost:26658",
		EnvVars: prefixEnvVars("CELESTIA_SERVER"),
	}
	CelestiaTLSEnabledFlag = &cli.BoolFlag{
		Name:    CelestiaTLSEnabledFlagName,
		Usage:   "celestia rpc TLS",
		EnvVars: prefixEnvVars("CELESTIA_TLS_ENABLED"),
		Value:   false,
	}
	CelestiaAuthTokenFlag = &cli.StringFlag{
		Name:    CelestiaAuthTokenFlagName,
		Usage:   "celestia rpc auth token",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_AUTH_TOKEN"),
	}
	CelestiaNamespaceFlag = &cli.StringFlag{
		Name:    CelestiaNamespaceFlagName,
		Usage:   "celestia namespace (29 bytes hex)",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_NAMESPACE"),
	}
	CelestiaBlobIDCompactFlag = &cli.BoolFlag{
		Name:    CelestiaCompactBlobIDFlagName,
		Usage:   "enable compact celestia blob IDs (height + commitment only)",
		Value:   true,
		EnvVars: prefixEnvVars("CELESTIA_BLOBID_COMPACT"),
	}
	CelestiaDefaultKeyNameFlag = &cli.StringFlag{
		Name:    CelestiaDefaultKeyNameFlagName,
		Usage:   "celestia tx client key name",
		Value:   "my_celes_key",
		EnvVars: prefixEnvVars("CELESTIA_TX_CLIENT_KEY_NAME"),
	}
	CelestiaKeyringPathFlag = &cli.StringFlag{
		Name:    CelestiaKeyringPathFlagName,
		Usage:   "celestia tx client keyring path e.g. ~/.celestia-light-mocha-4/keys",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_TX_CLIENT_KEYRING_PATH"),
	}
	CelestiaCoreGRPCAddrFlag = &cli.StringFlag{
		Name:    CelestiaCoreGRPCAddrFlagName,
		Usage:   "celestia tx client core grpc addr (for blob submission)",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_TX_CLIENT_CORE_GRPC_ADDR"),
	}
	CelestiaCoreGRPCTLSEnabledFlag = &cli.BoolFlag{
		Name:    CelestiaCoreGRPCTLSEnabledFlagName,
		Usage:   "celestia tx client core grpc TLS",
		EnvVars: prefixEnvVars("CELESTIA_TX_CLIENT_CORE_GRPC_TLS_ENABLED"),
		Value:   true,
	}
	CelestiaCoreGRPCAuthTokenFlag = &cli.StringFlag{
		Name:    CelestiaCoreGRPCAuthTokenFlagName,
		Usage:   "celestia tx client core grpc auth token",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_TX_CLIENT_CORE_GRPC_AUTH_TOKEN"),
	}
	CelestiaP2PNetworkFlag = &cli.StringFlag{
		Name:    CelestiaP2PNetworkFlagName,
		Usage:   "celestia tx client p2p network (mocha-4, arabica-11, celestia)",
		Value:   "mocha-4",
		EnvVars: prefixEnvVars("CELESTIA_TX_CLIENT_P2P_NETWORK"),
	}
	MetricsEnabledFlag = &cli.BoolFlag{
		Name:    MetricsEnabledFlagName,
		Usage:   "Enable Prometheus metrics",
		Value:   false,
		EnvVars: prefixEnvVars("METRICS_ENABLED"),
	}
	MetricsPortFlag = &cli.IntFlag{
		Name:    MetricsPortFlagName,
		Usage:   "Port for Prometheus metrics server",
		Value:   6060,
		EnvVars: prefixEnvVars("METRICS_PORT"),
	}

	// Fallback provider flags
	FallbackEnabledFlag = &cli.BoolFlag{
		Name:    FallbackEnabledFlagName,
		Usage:   "Enable fallback storage provider",
		Value:   false,
		EnvVars: prefixEnvVars("FALLBACK_ENABLED"),
	}
	FallbackProviderFlag = &cli.StringFlag{
		Name:    FallbackProviderFlagName,
		Usage:   "Fallback provider type (s3)",
		Value:   "s3",
		EnvVars: prefixEnvVars("FALLBACK_PROVIDER"),
	}
	FallbackS3BucketFlag = &cli.StringFlag{
		Name:    FallbackS3BucketFlagName,
		Usage:   "S3 bucket name for fallback storage",
		Value:   "",
		EnvVars: prefixEnvVars("FALLBACK_S3_BUCKET"),
	}
	FallbackS3PrefixFlag = &cli.StringFlag{
		Name:    FallbackS3PrefixFlagName,
		Usage:   "S3 key prefix for fallback storage",
		Value:   "",
		EnvVars: prefixEnvVars("FALLBACK_S3_PREFIX"),
	}
	FallbackS3EndpointFlag = &cli.StringFlag{
		Name:    FallbackS3EndpointFlagName,
		Usage:   "S3 endpoint URL (for S3-compatible services like MinIO)",
		Value:   "",
		EnvVars: prefixEnvVars("FALLBACK_S3_ENDPOINT"),
	}
	FallbackS3RegionFlag = &cli.StringFlag{
		Name:    FallbackS3RegionFlagName,
		Usage:   "S3 region",
		Value:   "us-east-1",
		EnvVars: prefixEnvVars("FALLBACK_S3_REGION"),
	}
	FallbackS3CredTypeFlag = &cli.StringFlag{
		Name:    FallbackS3CredTypeFlagName,
		Usage:   "S3 credential type: static, environment, iam",
		Value:   "",
		EnvVars: prefixEnvVars("FALLBACK_S3_CREDENTIAL_TYPE"),
	}
	FallbackS3AccessKeyFlag = &cli.StringFlag{
		Name:    FallbackS3AccessKeyFlagName,
		Usage:   "S3 access key ID",
		Value:   "",
		EnvVars: prefixEnvVars("FALLBACK_S3_ACCESS_KEY_ID"),
	}
	FallbackS3SecretKeyFlag = &cli.StringFlag{
		Name:    FallbackS3SecretKeyFlagName,
		Usage:   "S3 secret access key",
		Value:   "",
		EnvVars: prefixEnvVars("FALLBACK_S3_ACCESS_KEY_SECRET"),
	}
	FallbackS3TimeoutFlag = &cli.DurationFlag{
		Name:    FallbackS3TimeoutFlagName,
		Usage:   "S3 operation timeout",
		Value:   30 * time.Second,
		EnvVars: prefixEnvVars("FALLBACK_S3_TIMEOUT"),
	}
)

var requiredFlags = []cli.Flag{
	CelestiaNamespaceFlag,
}

var optionalFlags = []cli.Flag{
	ConfigFileFlag,
	ListenAddrFlag,
	PortFlag,
	CelestiaServerFlag,
	CelestiaTLSEnabledFlag,
	CelestiaAuthTokenFlag,
	CelestiaBlobIDCompactFlag,
	CelestiaDefaultKeyNameFlag,
	CelestiaKeyringPathFlag,
	CelestiaCoreGRPCAddrFlag,
	CelestiaCoreGRPCTLSEnabledFlag,
	CelestiaCoreGRPCAuthTokenFlag,
	CelestiaP2PNetworkFlag,
	MetricsEnabledFlag,
	MetricsPortFlag,
	// Fallback flags
	FallbackEnabledFlag,
	FallbackProviderFlag,
	FallbackS3BucketFlag,
	FallbackS3PrefixFlag,
	FallbackS3EndpointFlag,
	FallbackS3RegionFlag,
	FallbackS3CredTypeFlag,
	FallbackS3AccessKeyFlag,
	FallbackS3SecretKeyFlag,
	FallbackS3TimeoutFlag,
}

// Flags contains the list of configuration options available to the binary.
var Flags []cli.Flag

func init() {
	optionalFlags = append(optionalFlags, oplog.CLIFlags(EnvVarPrefix)...)
	Flags = append(requiredFlags, optionalFlags...)
}

// CLIFallbackConfig holds the CLI configuration for fallback storage providers.
type CLIFallbackConfig struct {
	Enabled  bool
	Provider string
	S3       s3.Config
}

// CLIConfig holds the configuration for the stateless DA server.
// This is a simplified config without batch, worker, backfill, or S3 sections.
type CLIConfig struct {
	// Server settings
	Addr string
	Port int

	// Celestia settings
	CelestiaEndpoint      string
	CelestiaTLSEnabled    bool
	CelestiaAuthToken     string
	CelestiaNamespace     string
	CelestiaCompactBlobID bool

	// TX client settings (for clientTX architecture)
	TxClientConfig celestia.TxClientConfig

	// Metrics settings
	MetricsEnabled bool
	MetricsPort    int

	// Fallback settings
	Fallback CLIFallbackConfig
}

// ReadCLIConfig reads CLI flags into a CLIConfig struct.
func ReadCLIConfig(ctx *cli.Context) CLIConfig {
	return CLIConfig{
		Addr:                  ctx.String(ListenAddrFlagName),
		Port:                  ctx.Int(PortFlagName),
		CelestiaEndpoint:      ctx.String(CelestiaServerFlagName),
		CelestiaTLSEnabled:    ctx.Bool(CelestiaTLSEnabledFlagName),
		CelestiaAuthToken:     ctx.String(CelestiaAuthTokenFlagName),
		CelestiaNamespace:     ctx.String(CelestiaNamespaceFlagName),
		CelestiaCompactBlobID: ctx.Bool(CelestiaCompactBlobIDFlagName),
		TxClientConfig: celestia.TxClientConfig{
			DefaultKeyName:     ctx.String(CelestiaDefaultKeyNameFlagName),
			KeyringPath:        ctx.String(CelestiaKeyringPathFlagName),
			CoreGRPCAddr:       ctx.String(CelestiaCoreGRPCAddrFlagName),
			CoreGRPCTLSEnabled: ctx.Bool(CelestiaCoreGRPCTLSEnabledFlagName),
			CoreGRPCAuthToken:  ctx.String(CelestiaCoreGRPCAuthTokenFlagName),
			P2PNetwork:         ctx.String(CelestiaP2PNetworkFlagName),
		},
		MetricsEnabled: ctx.Bool(MetricsEnabledFlagName),
		MetricsPort:    ctx.Int(MetricsPortFlagName),
		Fallback: CLIFallbackConfig{
			Enabled:  ctx.Bool(FallbackEnabledFlagName),
			Provider: ctx.String(FallbackProviderFlagName),
			S3: s3.Config{
				Bucket:          ctx.String(FallbackS3BucketFlagName),
				Prefix:          ctx.String(FallbackS3PrefixFlagName),
				Endpoint:        ctx.String(FallbackS3EndpointFlagName),
				Region:          ctx.String(FallbackS3RegionFlagName),
				CredentialType:  ctx.String(FallbackS3CredTypeFlagName),
				AccessKeyID:     ctx.String(FallbackS3AccessKeyFlagName),
				AccessKeySecret: ctx.String(FallbackS3SecretKeyFlagName),
				Timeout:         ctx.Duration(FallbackS3TimeoutFlagName),
			},
		},
	}
}

// TxClientEnabled returns true if the TX client (CoreGRPC) is enabled.
func (c CLIConfig) TxClientEnabled() bool {
	return c.TxClientConfig.KeyringPath != "" || c.TxClientConfig.CoreGRPCAuthToken != ""
}

// Check validates the CLI configuration.
func (c CLIConfig) Check() error {
	if c.TxClientEnabled() {
		// If tx client is enabled, ensure tx client flags are set
		if c.TxClientConfig.DefaultKeyName == "" {
			return errors.New("--celestia.tx-client.key-name must be set")
		}
		if c.TxClientConfig.KeyringPath == "" {
			return errors.New("--celestia.tx-client.keyring-path must be set")
		}
		if c.TxClientConfig.CoreGRPCAddr == "" {
			return errors.New("--celestia.tx-client.core-grpc.addr must be set")
		}
		if c.TxClientConfig.P2PNetwork == "" {
			return errors.New("--celestia.tx-client.p2p-network must be set")
		}
	}
	if _, err := hex.DecodeString(c.CelestiaNamespace); err != nil {
		return fmt.Errorf("celestia namespace: %w", err)
	}

	return nil
}

// CelestiaConfig converts CLIConfig to celestia.RPCClientConfig.
func (c CLIConfig) CelestiaConfig() celestia.RPCClientConfig {
	ns, _ := hex.DecodeString(c.CelestiaNamespace)
	var cfg *celestia.TxClientConfig
	if c.TxClientEnabled() {
		cfg = &c.TxClientConfig
	}
	return celestia.RPCClientConfig{
		URL:            c.CelestiaEndpoint,
		TLSEnabled:     c.CelestiaTLSEnabled,
		AuthToken:      c.CelestiaAuthToken,
		Namespace:      ns,
		CompactBlobID:  c.CelestiaCompactBlobID,
		TxClientConfig: cfg,
	}
}

// CelestiaRPCClientEnabled returns true if Celestia RPC client should be used.
func (c CLIConfig) CelestiaRPCClientEnabled() bool {
	return !(c.CelestiaEndpoint == "" && c.CelestiaAuthToken == "" && c.CelestiaNamespace == "")
}

// CheckRequired verifies all required flags are set.
func CheckRequired(ctx *cli.Context) error {
	// If config file is provided, namespace can come from there
	if ctx.IsSet(ConfigFileFlagName) {
		return nil
	}
	for _, f := range requiredFlags {
		if !ctx.IsSet(f.Names()[0]) {
			return fmt.Errorf("flag %s is required", f.Names()[0])
		}
	}
	return nil
}
