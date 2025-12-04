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
	ListenAddrFlagName = "addr"
	PortFlagName       = "port"

	// Bridge node settings (for reading blobs via JSON-RPC)
	CelestiaBridgeAddrFlagName       = "celestia.bridge-addr"
	CelestiaBridgeTLSEnabledFlagName = "celestia.bridge-tls-enabled"
	CelestiaBridgeAuthTokenFlagName  = "celestia.bridge-auth-token"

	// CoreGRPC settings (for submitting blobs)
	CelestiaCoreGRPCAddrFlagName       = "celestia.core-grpc-addr"
	CelestiaCoreGRPCTLSEnabledFlagName = "celestia.core-grpc-tls-enabled"
	CelestiaCoreGRPCAuthTokenFlagName  = "celestia.core-grpc-auth-token"

	// Keyring settings (for signing transactions)
	CelestiaKeyringPathFlagName    = "celestia.keyring-path"
	CelestiaDefaultKeyNameFlagName = "celestia.key-name"
	CelestiaP2PNetworkFlagName     = "celestia.p2p-network"

	// Blob settings
	CelestiaNamespaceFlagName     = "celestia.namespace"
	CelestiaCompactBlobIDFlagName = "celestia.compact-blobid"

	// Parallel submission settings
	CelestiaTxWorkerAccountsFlagName = "celestia.tx-worker-accounts"

	//s3
	S3CredentialTypeFlagName  = "s3.credential-type" // #nosec G101
	S3BucketFlagName          = "s3.bucket"          // #nosec G101
	S3PathFlagName            = "s3.path"
	S3EndpointFlagName        = "s3.endpoint"
	S3AccessKeyIDFlagName     = "s3.access-key-id"     // #nosec G101
	S3AccessKeySecretFlagName = "s3.access-key-secret" // #nosec G101
	S3TimeoutFlagName         = "s3.timeout"

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
	// Bridge node flags (for reading blobs)
	CelestiaBridgeAddrFlag = &cli.StringFlag{
		Name:    CelestiaBridgeAddrFlagName,
		Usage:   "celestia bridge node JSON-RPC endpoint for reading blobs",
		Value:   "http://localhost:26658",
		EnvVars: prefixEnvVars("CELESTIA_BRIDGE_ADDR"),
	}
	CelestiaBridgeTLSEnabledFlag = &cli.BoolFlag{
		Name:    CelestiaBridgeTLSEnabledFlagName,
		Usage:   "enable TLS for bridge node connection",
		EnvVars: prefixEnvVars("CELESTIA_BRIDGE_TLS_ENABLED"),
		Value:   false,
	}
	CelestiaBridgeAuthTokenFlag = &cli.StringFlag{
		Name:    CelestiaBridgeAuthTokenFlagName,
		Usage:   "auth token for bridge node JSON-RPC (optional, some providers require it)",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_BRIDGE_AUTH_TOKEN"),
	}
	// CoreGRPC flags (for submitting blobs)
	CelestiaCoreGRPCAddrFlag = &cli.StringFlag{
		Name:    CelestiaCoreGRPCAddrFlagName,
		Usage:   "celestia consensus node gRPC endpoint for submitting blobs",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_CORE_GRPC_ADDR"),
	}
	CelestiaCoreGRPCTLSEnabledFlag = &cli.BoolFlag{
		Name:    CelestiaCoreGRPCTLSEnabledFlagName,
		Usage:   "enable TLS for gRPC connection",
		EnvVars: prefixEnvVars("CELESTIA_CORE_GRPC_TLS_ENABLED"),
		Value:   true,
	}
	CelestiaCoreGRPCAuthTokenFlag = &cli.StringFlag{
		Name:    CelestiaCoreGRPCAuthTokenFlagName,
		Usage:   "auth token for gRPC (optional, some providers like QuickNode require it)",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_CORE_GRPC_AUTH_TOKEN"),
	}
	// Keyring flags (for signing)
	CelestiaKeyringPathFlag = &cli.StringFlag{
		Name:    CelestiaKeyringPathFlagName,
		Usage:   "path to keyring directory (e.g., ~/.celestia-light-mocha-4/keys)",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_KEYRING_PATH"),
	}
	CelestiaDefaultKeyNameFlag = &cli.StringFlag{
		Name:    CelestiaDefaultKeyNameFlagName,
		Usage:   "key name to use for signing transactions",
		Value:   "my_celes_key",
		EnvVars: prefixEnvVars("CELESTIA_KEY_NAME"),
	}
	CelestiaP2PNetworkFlag = &cli.StringFlag{
		Name:    CelestiaP2PNetworkFlagName,
		Usage:   "celestia p2p network (mocha-4, arabica-11, mainnet)",
		Value:   "mocha-4",
		EnvVars: prefixEnvVars("CELESTIA_P2P_NETWORK"),
	}
	// Blob settings
	CelestiaNamespaceFlag = &cli.StringFlag{
		Name:    CelestiaNamespaceFlagName,
		Usage:   "celestia namespace",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_NAMESPACE"),
	}
	CelestiaBlobIDCompactFlag = &cli.BoolFlag{
		Name:    CelestiaCompactBlobIDFlagName,
		Usage:   "enable compact celestia blob IDs",
		Value:   true,
		EnvVars: prefixEnvVars("CELESTIA_BLOBID_COMPACT"),
	}
	// Parallel submission flag
	CelestiaTxWorkerAccountsFlag = &cli.IntFlag{
		Name:    CelestiaTxWorkerAccountsFlagName,
		Usage:   "number of worker accounts for parallel transaction submission (0=immediate, 1=synchronous, >1=parallel). Run 'da-server init' first to see worker addresses for trusted_signers",
		Value:   0,
		EnvVars: prefixEnvVars("CELESTIA_TX_WORKER_ACCOUNTS"),
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
	ConfigFileFlag,
	ListenAddrFlag,
	PortFlag,
	// Bridge node flags
	CelestiaBridgeAddrFlag,
	CelestiaBridgeTLSEnabledFlag,
	CelestiaBridgeAuthTokenFlag,
	// CoreGRPC flags
	CelestiaCoreGRPCAddrFlag,
	CelestiaCoreGRPCTLSEnabledFlag,
	CelestiaCoreGRPCAuthTokenFlag,
	// Keyring flags
	CelestiaKeyringPathFlag,
	CelestiaDefaultKeyNameFlag,
	CelestiaP2PNetworkFlag,
	// Blob settings (CelestiaNamespaceFlag is in requiredFlags)
	CelestiaBlobIDCompactFlag,
	// Parallel submission
	CelestiaTxWorkerAccountsFlag,
	S3CredentialTypeFlag,
	S3BucketFlag,
	S3PathFlag,
	S3EndpointFlag,
	S3AccessKeyIDFlag,
	S3AccessKeySecretFlag,
	S3TimeoutFlag,
	MetricsEnabledFlag,
	MetricsPortFlag,
}

// Flags contains the list of configuration options available to the binary.
var Flags []cli.Flag

func init() {
	optionalFlags = append(optionalFlags, oplog.CLIFlags(EnvVarPrefix)...)
	Flags = append(requiredFlags, optionalFlags...)
}

// CLIConfig holds the CLI configuration for the DA server.
// Uses the unified CelestiaClientConfig which combines bridge node and CoreGRPC settings.
type CLIConfig struct {
	CelestiaConfig celestia.CelestiaClientConfig
	S3Config       s3.S3Config
	MetricsEnabled bool
	MetricsPort    int
}

func ReadCLIConfig(ctx *cli.Context) CLIConfig {
	ns, _ := hex.DecodeString(ctx.String(CelestiaNamespaceFlagName))
	return CLIConfig{
		CelestiaConfig: celestia.CelestiaClientConfig{
			// Read-only mode
			ReadOnly: ctx.Bool(ReadOnlyFlagName),
			// Bridge node settings
			BridgeAddr:       ctx.String(CelestiaBridgeAddrFlagName),
			BridgeAuthToken:  ctx.String(CelestiaBridgeAuthTokenFlagName),
			BridgeTLSEnabled: ctx.Bool(CelestiaBridgeTLSEnabledFlagName),
			// CoreGRPC settings
			CoreGRPCAddr:       ctx.String(CelestiaCoreGRPCAddrFlagName),
			CoreGRPCAuthToken:  ctx.String(CelestiaCoreGRPCAuthTokenFlagName),
			CoreGRPCTLSEnabled: ctx.Bool(CelestiaCoreGRPCTLSEnabledFlagName),
			// Keyring settings
			KeyringPath:    ctx.String(CelestiaKeyringPathFlagName),
			DefaultKeyName: ctx.String(CelestiaDefaultKeyNameFlagName),
			P2PNetwork:     ctx.String(CelestiaP2PNetworkFlagName),
			// Parallel submission settings
			TxWorkerAccounts: ctx.Int(CelestiaTxWorkerAccountsFlagName),
			// Blob settings
			Namespace:     ns,
			CompactBlobID: ctx.Bool(CelestiaCompactBlobIDFlagName),
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
		MetricsEnabled: ctx.Bool(MetricsEnabledFlagName),
		MetricsPort:    ctx.Int(MetricsPortFlagName),
	}
}

func (c CLIConfig) Check(readOnly bool) error {
	cfg := c.CelestiaConfig

	// Validate namespace
	if len(cfg.Namespace) == 0 {
		return errors.New("--celestia.namespace is required")
	}

	// Validate bridge node endpoint (required for reading blobs in all modes)
	if cfg.BridgeAddr == "" {
		return errors.New("--celestia.bridge-addr is required for reading blobs")
	}

	// Write mode requires CoreGRPC and keyring for submitting blobs
	if !readOnly {
		if cfg.CoreGRPCAddr == "" {
			return errors.New("--celestia.core-grpc-addr is required for submitting blobs (not needed with --read-only)")
		}
		if cfg.KeyringPath == "" {
			return errors.New("--celestia.keyring-path is required for signing transactions (not needed with --read-only)")
		}
		if cfg.P2PNetwork == "" {
			return errors.New("--celestia.p2p-network must be set (not needed with --read-only)")
		}
		if cfg.DefaultKeyName == "" {
			return errors.New("--celestia.key-name must be set (not needed with --read-only)")
		}
	}

	// S3 config validation - only validate if bucket is set (S3 is being used)
	if c.S3Config.Bucket != "" {
		if c.S3Config.S3CredentialType == s3.S3CredentialUnknown {
			return errors.New("s3: credential_type must be set to 'iam' or 'static' when bucket is configured")
		}
		if c.S3Config.S3CredentialType == s3.S3CredentialStatic {
			if c.S3Config.AccessKeyID == "" || c.S3Config.AccessKeySecret == "" {
				return errors.New("s3 static credentials: access_key_id and access_key_secret must be set")
			}
		}
	}

	return nil
}

// GetCelestiaMode returns a human-readable description of the Celestia connection mode
func (c CLIConfig) GetCelestiaMode() string {
	return "Dual-endpoint mode (reads via bridge node JSON-RPC, writes via CoreGRPC)"
}

// GetCelestiaModeDetails returns detailed configuration for Celestia connection
func (c CLIConfig) GetCelestiaModeDetails() map[string]string {
	cfg := c.CelestiaConfig
	details := make(map[string]string)

	details["mode"] = "Dual-endpoint"
	details["description"] = "Reads via bridge node, writes via CoreGRPC"
	details["bridge_addr"] = cfg.BridgeAddr
	details["grpc_addr"] = cfg.CoreGRPCAddr
	details["keyring_path"] = cfg.KeyringPath
	details["key_name"] = cfg.DefaultKeyName
	details["p2p_network"] = cfg.P2PNetwork
	if cfg.BridgeAuthToken != "" {
		details["bridge_auth_token"] = "<redacted>"
	}
	if cfg.CoreGRPCAuthToken != "" {
		details["grpc_auth_token"] = "<redacted>"
	}

	return details
}

// GetCelestiaClientConfig returns the Celestia client configuration
func (c CLIConfig) GetCelestiaClientConfig() celestia.CelestiaClientConfig {
	return c.CelestiaConfig
}

func (c CLIConfig) CelestiaClientEnabled() bool {
	cfg := c.CelestiaConfig
	return cfg.BridgeAddr != "" && len(cfg.Namespace) > 0 && cfg.KeyringPath != "" && cfg.CoreGRPCAddr != ""
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
