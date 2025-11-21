package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/urfave/cli/v2"

	s3 "github.com/celestiaorg/op-alt-da/s3"

	celestia "github.com/celestiaorg/op-alt-da"
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

	//s3
	S3CredentialTypeFlagName  = "s3.credential-type" // #nosec G101
	S3BucketFlagName          = "s3.bucket"          // #nosec G101
	S3PathFlagName            = "s3.path"
	S3EndpointFlagName        = "s3.endpoint"
	S3AccessKeyIDFlagName     = "s3.access-key-id"     // #nosec G101
	S3AccessKeySecretFlagName = "s3.access-key-secret" // #nosec G101
	S3TimeoutFlagName         = "s3.timeout"
	FallbackFlagName          = "routing.fallback"
	CacheFlagName             = "routing.cache"

	// metrics
	MetricsEnabledFlagName = "metrics.enabled"
	MetricsPortFlagName    = "metrics.port"
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
		Usage:   "celestia rpc endpoint",
		Value:   "http://localhost:26658",
		EnvVars: prefixEnvVars("CELESTIA_SERVER"),
	}
	CelestiaTLSEnabledFlag = &cli.BoolFlag{
		Name:    CelestiaTLSEnabledFlagName,
		Usage:   "celestia rpc TLS",
		EnvVars: prefixEnvVars("CELESTIA_TLS_ENABLED"),
		Value:   true,
	}
	CelestiaAuthTokenFlag = &cli.StringFlag{
		Name:    CelestiaAuthTokenFlagName,
		Usage:   "celestia rpc auth token",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_AUTH_TOKEN"),
	}
	CelestiaNamespaceFlag = &cli.StringFlag{
		Name:    CelestiaNamespaceFlagName,
		Usage:   "celestia namespace",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_NAMESPACE"),
	}
	CelestiaBlobIDCompactFlag = &cli.BoolFlag{
		Name:    CelestiaCompactBlobIDFlagName,
		Usage:   "enable compact celestia blob IDs. false indicates share offset and size will be included in the blob ID",
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
		Usage:   "celestia tx client core grpc addr",
		Value:   "http://localhost:9090",
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
		Usage:   "celestia tx client p2p network",
		Value:   "mocha-4",
		EnvVars: prefixEnvVars("CELESTIA_TX_CLIENT_P2P_NETWORK"),
	}
	S3CredentialTypeFlag = &cli.StringFlag{
		Name:    S3CredentialTypeFlagName,
		Usage:   "The way to authenticate to S3, options are [iam, static]",
		EnvVars: prefixEnvVars("S3_CREDENTIAL_TYPE"),
	}
	S3BucketFlag = &cli.StringFlag{
		Name:    S3BucketFlagName,
		Usage:   "bucket name for S3 storage",
		EnvVars: prefixEnvVars("S3_BUCKET"),
	}
	S3PathFlag = &cli.StringFlag{
		Name:    S3PathFlagName,
		Usage:   "path for S3 storage",
		EnvVars: prefixEnvVars("S3_PATH"),
	}
	S3EndpointFlag = &cli.StringFlag{
		Name:    S3EndpointFlagName,
		Usage:   "endpoint for S3 storage",
		Value:   "",
		EnvVars: prefixEnvVars("S3_ENDPOINT"),
	}
	S3AccessKeyIDFlag = &cli.StringFlag{
		Name:    S3AccessKeyIDFlagName,
		Usage:   "access key id for S3 storage",
		Value:   "",
		EnvVars: prefixEnvVars("S3_ACCESS_KEY_ID"),
	}
	S3AccessKeySecretFlag = &cli.StringFlag{
		Name:    S3AccessKeySecretFlagName,
		Usage:   "access key secret for S3 storage",
		Value:   "",
		EnvVars: prefixEnvVars("S3_ACCESS_KEY_SECRET"),
	}
	S3TimeoutFlag = &cli.DurationFlag{
		Name:    S3TimeoutFlagName,
		Usage:   "S3 timeout",
		Value:   5 * time.Second,
		EnvVars: prefixEnvVars("S3_TIMEOUT"),
	}
	FallbackFlag = &cli.BoolFlag{
		Name:    FallbackFlagName,
		Usage:   "Enable fallback",
		Value:   false,
		EnvVars: prefixEnvVars("FALLBACK"),
	}
	CacheFlag = &cli.BoolFlag{
		Name:    CacheFlagName,
		Usage:   "Enable cache.",
		Value:   false,
		EnvVars: prefixEnvVars("CACHE"),
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
)
var requiredFlags = []cli.Flag{
	CelestiaNamespaceFlag,
}

var optionalFlags = []cli.Flag{
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
	S3CredentialTypeFlag,
	S3BucketFlag,
	S3PathFlag,
	S3EndpointFlag,
	S3AccessKeyIDFlag,
	S3AccessKeySecretFlag,
	S3TimeoutFlag,
	FallbackFlag,
	CacheFlag,
	MetricsEnabledFlag,
	MetricsPortFlag,
}

// Flags contains the list of configuration options available to the binary.
var Flags []cli.Flag

func init() {
	optionalFlags = append(optionalFlags, oplog.CLIFlags(EnvVarPrefix)...)
	Flags = append(requiredFlags, optionalFlags...)
}

type CLIConfig struct {
	CelestiaEndpoint      string
	CelestiaTLSEnabled    bool
	CelestiaAuthToken     string
	CelestiaNamespace     string
	CelestiaCompactBlobID bool
	TxClientConfig        celestia.TxClientConfig
	S3Config              s3.S3Config
	Fallback              bool
	Cache                 bool
	MetricsEnabled        bool
	MetricsPort           int
}

func ReadCLIConfig(ctx *cli.Context) CLIConfig {
	return CLIConfig{
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
		S3Config: s3.S3Config{
			S3CredentialType: toS3CredentialType(ctx.String(S3CredentialTypeFlagName)),
			Bucket:           ctx.String(S3BucketFlagName),
			Path:             ctx.String(S3PathFlagName),
			Endpoint:         ctx.String(S3EndpointFlagName),
			AccessKeyID:      ctx.String(S3AccessKeyIDFlagName),
			AccessKeySecret:  ctx.String(S3AccessKeySecretFlagName),
			Timeout:          ctx.Duration(S3TimeoutFlagName),
		},
		Fallback:       ctx.Bool(FallbackFlagName),
		Cache:          ctx.Bool(CacheFlagName),
		MetricsEnabled: ctx.Bool(MetricsEnabledFlagName),
		MetricsPort:    ctx.Int(MetricsPortFlagName),
	}
}

func (c CLIConfig) TxClientEnabled() bool {
	return c.TxClientConfig.KeyringPath != "" || c.TxClientConfig.CoreGRPCAuthToken != ""
}

func (c CLIConfig) Check() error {
	// Validate namespace
	if _, err := hex.DecodeString(c.CelestiaNamespace); err != nil {
		return fmt.Errorf("celestia namespace: %w", err)
	}

	// Detect which mode is being used
	hasAuthToken := c.CelestiaAuthToken != ""
	hasKeyringPath := c.TxClientConfig.KeyringPath != ""
	hasGRPCAddr := c.TxClientConfig.CoreGRPCAddr != ""

	// Determine mode and validate
	if hasKeyringPath || hasGRPCAddr {
		// OPTION B: Service Provider (client-tx mode)
		// Requires: RPC endpoint (reads) + gRPC endpoint (writes) + keyring

		if c.CelestiaEndpoint == "" {
			return errors.New("OPTION B (client-tx): --celestia.server (RPC endpoint) is required for reads (Get, GetAll, Subscribe)")
		}

		if c.TxClientConfig.KeyringPath == "" {
			return errors.New("OPTION B (client-tx): --celestia.tx-client.keyring-path must be set for transaction signing")
		}

		if c.TxClientConfig.CoreGRPCAddr == "" {
			return errors.New("OPTION B (client-tx): --celestia.tx-client.core-grpc.addr (gRPC endpoint) is required for writes (Submit)")
		}

		if c.TxClientConfig.P2PNetwork == "" {
			return errors.New("OPTION B (client-tx): --celestia.tx-client.p2p-network must be set (e.g., mocha-4, arabica-11, mainnet)")
		}

		if c.TxClientConfig.DefaultKeyName == "" {
			return errors.New("OPTION B (client-tx): --celestia.tx-client.key-name must be set")
		}

		// Warn if auth token is also set (mixing configurations)
		if hasAuthToken {
			return errors.New("configuration conflict: detected both --celestia.auth-token (OPTION A) and client-tx settings (OPTION B). Please use ONLY one mode. See .env.example for guidance")
		}

	} else if hasAuthToken {
		// OPTION A: Local Celestia Node
		// Requires: RPC endpoint + auth token

		if c.CelestiaEndpoint == "" {
			return errors.New("OPTION A (local node): --celestia.server (RPC endpoint) is required")
		}

	} else {
		// Neither mode configured properly
		return errors.New(`celestia connection not configured. Choose ONE mode:

OPTION A (Local Node): Set --celestia.auth-token
  Example: --celestia.server http://localhost:26658 --celestia.auth-token <token>

OPTION B (Service Provider): Set --celestia.tx-client.keyring-path and --celestia.tx-client.core-grpc.addr
  Example: --celestia.server https://rpc.endpoint --celestia.tx-client.core-grpc.addr grpc.endpoint:9090 --celestia.tx-client.keyring-path ~/.celestia-light-mocha-4/keys --celestia.tx-client.p2p-network mocha-4

See .env.example for detailed configuration examples.`)
	}

	// S3 config validation (existing logic)
	if c.S3Config.S3CredentialType != s3.S3CredentialUnknown && c.S3Config.Bucket == "" {
		return errors.New("s3: bucket must be set when S3 is enabled")
	}
	if c.S3Config.S3CredentialType == s3.S3CredentialStatic {
		if c.S3Config.AccessKeyID == "" || c.S3Config.AccessKeySecret == "" {
			return errors.New("s3 static credentials: access key ID and secret must be set")
		}
	}

	return nil
}

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

func (c CLIConfig) CelestiaRPCClientEnabled() bool {
	return !(c.CelestiaEndpoint == "" && c.CelestiaAuthToken == "" && c.CelestiaNamespace == "")
}

func (c CLIConfig) CacheEnabled() bool {
	return c.Cache
}

func (c CLIConfig) FallbackEnabled() bool {
	return c.Fallback
}

func toS3CredentialType(s string) s3.S3CredentialType {
	if s == string(s3.S3CredentialStatic) {
		return s3.S3CredentialStatic
	} else if s == string(s3.S3CredentialIAM) {
		return s3.S3CredentialIAM
	}
	return s3.S3CredentialUnknown
}

func CheckRequired(ctx *cli.Context) error {
	for _, f := range requiredFlags {
		if !ctx.IsSet(f.Names()[0]) {
			return fmt.Errorf("flag %s is required", f.Names()[0])
		}
	}
	return nil
}
