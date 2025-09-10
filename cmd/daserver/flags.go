package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	s3 "github.com/celestiaorg/op-alt-da/s3"

	celestia "github.com/celestiaorg/op-alt-da"
	opservice "github.com/ethereum-optimism/optimism/op-service"
	oplog "github.com/ethereum-optimism/optimism/op-service/log"
)

const (
	ListenAddrFlagName                 = "addr"
	PortFlagName                       = "port"
	CelestiaAddrFlagName               = "celestia.addr"
	CelestiaTLSEnabledFlagName         = "celestia.tls-enabled"
	CelestiaAuthTokenFlagName          = "celestia.auth-token"
	CelestiaNamespaceFlagName          = "celestia.namespace"
	CelestiaDefaultKeyNameFlagName     = "celestia.keyring.default-key-name"
	CelestiaKeyringPathFlagName        = "celestia.keyring.path"
	CelestiaCoreGRPCAddrFlagName       = "celestia.core.grpc.addr"
	CelestiaCoreGRPCTLSEnabledFlagName = "celestia.core.grpc.tls-enabled"
	CelestiaCoreGRPCAuthTokenFlagName  = "celestia.core.grpc.auth-token"
	CelestiaP2PNetworkFlagName         = "celestia.p2p-network"
	CelestiaCompactBlobIDFlagName      = "celestia.compact-blobid"
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
)

const EnvVarPrefix = "OP_ALTDA"

func prefixEnvVars(name string) []string {
	return opservice.PrefixEnvVar(EnvVarPrefix, name)
}

// DefaultKeyringPath constructs the default keyring path using the given
// node type and network.
var DefaultKeyringPath = func(tp string, network string) (string, error) {
	home := os.Getenv("CELESTIA_HOME")
	if home != "" {
		return home, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	if network == "mainnet" {
		return fmt.Sprintf("%s/.celestia-%s/keys", home, strings.ToLower(tp)), nil
	}
	// only include network name in path for testnets and custom networks
	return fmt.Sprintf(
		"%s/.celestia-%s-%s/keys",
		home,
		strings.ToLower(tp),
		strings.ToLower(network),
	), nil
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
	CelestiaAddrFlag = &cli.StringFlag{
		Name:    CelestiaAddrFlagName,
		Usage:   "celestia rpc endpoint",
		Value:   "http://localhost:26658",
		EnvVars: prefixEnvVars("CELESTIA_ADDR"),
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
	CelestiaDefaultKeyNameFlag = &cli.StringFlag{
		Name:    CelestiaDefaultKeyNameFlagName,
		Usage:   "celestia default key name",
		Value:   "my_celes_key",
		EnvVars: prefixEnvVars("CELESTIA_DEFAULT_KEY_NAME"),
	}
	CelestiaKeyringPathFlag = &cli.StringFlag{
		Name:    CelestiaKeyringPathFlagName,
		Usage:   "celestia keyring path e.g. ~/.celestia-light-mocha-4/keys",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_KEYRING_PATH"),
	}
	CelestiaCoreGRPCAddrFlag = &cli.StringFlag{
		Name:    CelestiaCoreGRPCAddrFlagName,
		Usage:   "celestia core grpc addr",
		Value:   "http://localhost:9090",
		EnvVars: prefixEnvVars("CELESTIA_CORE_GRPC_ADDR"),
	}
	CelestiaCoreGRPCTLSEnabledFlag = &cli.BoolFlag{
		Name:    CelestiaCoreGRPCTLSEnabledFlagName,
		Usage:   "enable core grpc TLS",
		EnvVars: prefixEnvVars("CELESTIA_CORE_GRPC_TLS_ENABLED"),
		Value:   true,
	}
	CelestiaCoreGRPCAuthTokenFlag = &cli.StringFlag{
		Name:    CelestiaCoreGRPCAuthTokenFlagName,
		Usage:   "celestia core grpc auth token",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_CORE_GRPC_AUTH_TOKEN"),
	}
	CelestiaP2PNetworkFlag = &cli.StringFlag{
		Name:    CelestiaP2PNetworkFlagName,
		Usage:   "celestia p2p network",
		Value:   "mocha-4",
		EnvVars: prefixEnvVars("CELESTIA_P2P_NETWORK"),
		Action: func(c *cli.Context, network string) error {
			keyringPath, err := DefaultKeyringPath("light", network)
			if err != nil {
				return err
			}
			c.Set(CelestiaKeyringPathFlagName, keyringPath)
			return nil
		},
	}
	CelestiaBlobIDCompactFlag = &cli.BoolFlag{
		Name:    CelestiaCompactBlobIDFlagName,
		Usage:   "enable compact celestia blob IDs. false indicates share offset and size will be included in the blob ID",
		Value:   true,
		EnvVars: prefixEnvVars("CELESTIA_BLOBID_COMPACT"),
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
)
var requiredFlags = []cli.Flag{
	CelestiaAddrFlag,
	CelestiaAuthTokenFlag,
	CelestiaNamespaceFlag,
	CelestiaCoreGRPCAddrFlag,
	CelestiaP2PNetworkFlag,
}

var optionalFlags = []cli.Flag{
	ListenAddrFlag,
	PortFlag,
	CelestiaTLSEnabledFlag,
	CelestiaCoreGRPCTLSEnabledFlag,
	CelestiaCoreGRPCAuthTokenFlag,
	CelestiaDefaultKeyNameFlag,
	CelestiaKeyringPathFlag,
	S3CredentialTypeFlag,
	S3BucketFlag,
	S3PathFlag,
	S3EndpointFlag,
	S3AccessKeyIDFlag,
	S3AccessKeySecretFlag,
	S3TimeoutFlag,
	FallbackFlag,
	CacheFlag,
	CelestiaBlobIDCompactFlag,
}

func init() {
	optionalFlags = append(optionalFlags, oplog.CLIFlags(EnvVarPrefix)...)
	Flags = append(requiredFlags, optionalFlags...)
}

// Flags contains the list of configuration options available to the binary.
var Flags []cli.Flag

type CLIConfig struct {
	CelestiaEndpoint      string
	CelestiaTLSEnabled    bool
	CelestiaAuthToken     string
	CelestiaNamespace     string
	DefaultKeyName        string
	KeyringPath           string
	CoreGRPCAddr          string
	CoreGRPCTLSEnabled    bool
	CoreGRPCAuthToken     string
	P2PNetwork            string
	CelestiaCompactBlobID bool
	S3Config              s3.S3Config
	Fallback              bool
	Cache                 bool
}

func ReadCLIConfig(ctx *cli.Context) CLIConfig {
	return CLIConfig{
		CelestiaEndpoint:      ctx.String(CelestiaAddrFlagName),
		CelestiaTLSEnabled:    ctx.Bool(CelestiaTLSEnabledFlagName),
		CelestiaAuthToken:     ctx.String(CelestiaAuthTokenFlagName),
		CelestiaNamespace:     ctx.String(CelestiaNamespaceFlagName),
		DefaultKeyName:        ctx.String(CelestiaDefaultKeyNameFlagName),
		KeyringPath:           ctx.String(CelestiaKeyringPathFlagName),
		CoreGRPCAddr:          ctx.String(CelestiaCoreGRPCAddrFlagName),
		CoreGRPCTLSEnabled:    ctx.Bool(CelestiaCoreGRPCTLSEnabledFlagName),
		CoreGRPCAuthToken:     ctx.String(CelestiaCoreGRPCAuthTokenFlagName),
		P2PNetwork:            ctx.String(CelestiaP2PNetworkFlagName),
		CelestiaCompactBlobID: ctx.Bool(CelestiaCompactBlobIDFlagName),
		S3Config: s3.S3Config{
			S3CredentialType: toS3CredentialType(ctx.String(S3CredentialTypeFlagName)),
			Bucket:           ctx.String(S3BucketFlagName),
			Path:             ctx.String(S3PathFlagName),
			Endpoint:         ctx.String(S3EndpointFlagName),
			AccessKeyID:      ctx.String(S3AccessKeyIDFlagName),
			AccessKeySecret:  ctx.String(S3AccessKeySecretFlagName),
			Timeout:          ctx.Duration(S3TimeoutFlagName),
		},
		Fallback: ctx.Bool(FallbackFlagName),
		Cache:    ctx.Bool(CacheFlagName),
	}
}

func (c CLIConfig) Check() error {
	if c.CelestiaEnabled() && (c.CelestiaEndpoint == "" || c.CelestiaAuthToken == "" || c.CelestiaNamespace == "") {
		return errors.New("all Celestia flags must be set")
	}
	if c.CelestiaEnabled() {
		if _, err := hex.DecodeString(c.CelestiaNamespace); err != nil {
			return err
		}
	}
	return nil
}

func (c CLIConfig) CelestiaConfig() celestia.CelestiaConfig {
	ns, _ := hex.DecodeString(c.CelestiaNamespace)
	return celestia.CelestiaConfig{
		URL:                c.CelestiaEndpoint,
		TLSEnabled:         c.CelestiaTLSEnabled,
		AuthToken:          c.CelestiaAuthToken,
		Namespace:          ns,
		DefaultKeyName:     c.DefaultKeyName,
		KeyringPath:        c.KeyringPath,
		CoreGRPCAddr:       c.CoreGRPCAddr,
		CoreGRPCTLSEnabled: c.CoreGRPCTLSEnabled,
		CoreGRPCAuthToken:  c.CoreGRPCAuthToken,
		P2PNetwork:         c.P2PNetwork,
		CompactBlobID:      c.CelestiaCompactBlobID,
	}
}

func (c CLIConfig) CelestiaEnabled() bool {
	return !(c.CelestiaEndpoint == "" && c.CelestiaAuthToken == "" && c.CelestiaNamespace == "")
}

func (c CLIConfig) CacheEnabled() bool {
	return c.Cache
}

func (c CLIConfig) FallbackEnabled() bool {
	return c.Fallback
}

func CheckRequired(ctx *cli.Context) error {
	for _, f := range requiredFlags {
		if !ctx.IsSet(f.Names()[0]) {
			return fmt.Errorf("flag %s is required", f.Names()[0])
		}
	}
	return nil
}

func toS3CredentialType(s string) s3.S3CredentialType {
	if s == string(s3.S3CredentialStatic) {
		return s3.S3CredentialStatic
	} else if s == string(s3.S3CredentialIAM) {
		return s3.S3CredentialIAM
	}
	return s3.S3CredentialUnknown
}
