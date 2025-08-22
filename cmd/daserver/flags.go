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
	ListenAddrFlagName                 = "addr"
	PortFlagName                       = "port"
	GenericCommFlagName                = "generic-commitment"
	CelestiaBridgeAddrFlagName         = "celestia.bridge.addr"
	CelestiaBridgeDATLSEnabledFlagName = "celestia.bridge.tls.enabled"
	CelestiaBridgeAuthTokenFlagName    = "celestia.bridge.auth-token"
	CelestiaNamespaceFlagName          = "celestia.namespace"
	CelestiaDefaultKeyNameFlagName     = "celestia.keyring.default-key-name"
	CelestiaKeyringPathFlagName        = "celestia.keyring.path"
	CelestiaCoreGRPCAddrFlagName       = "celestia.core.grpc.addr"
	CelestiaCoreGRPCTLSEnabledFlagName = "celestia.core.grpc.tls-enabled"
	CelestiaCoreGRPCAuthTokenFlagName  = "celestia.core.grpc.auth-token"
	CelestiaP2PNetworkFlagName         = "celestia.p2p-network"
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

const EnvVarPrefix = "OP_ALT_DA_SERVER"

func prefixEnvVars(name string) []string {
	return opservice.PrefixEnvVar(EnvVarPrefix, name)
}

var (
	ListenAddrFlag = &cli.StringFlag{
		Name:    ListenAddrFlagName,
		Usage:   "server listening address",
		Value:   "127.0.0.1",
		EnvVars: prefixEnvVars("ADDR"),
	}
	PortFlag = &cli.IntFlag{
		Name:    PortFlagName,
		Usage:   "server listening port",
		Value:   3100,
		EnvVars: prefixEnvVars("PORT"),
	}
	GenericCommFlag = &cli.BoolFlag{
		Name:    GenericCommFlagName,
		Usage:   "enable generic commitments for testing. Not for production use.",
		EnvVars: prefixEnvVars("GENERIC_COMMITMENT"),
		Value:   true,
	}
	CelestiaBridgeAddrFlag = &cli.StringFlag{
		Name:    CelestiaBridgeAddrFlagName,
		Usage:   "celestia server endpoint",
		Value:   "http://localhost:26658",
		EnvVars: prefixEnvVars("CELESTIA_BRIDGE_ADDR"),
	}
	CelestiaBridgeDATLSEnabledFlag = &cli.BoolFlag{
		Name:    CelestiaBridgeDATLSEnabledFlagName,
		Usage:   "enable DA TLS",
		EnvVars: prefixEnvVars("CELESTIA_BRIDGE_TLS_ENABLED"),
		Value:   false,
	}
	CelestiaBridgeAuthTokenFlag = &cli.StringFlag{
		Name:    CelestiaBridgeAuthTokenFlagName,
		Usage:   "celestia auth token",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_BRIDGE_AUTH_TOKEN"),
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
		Usage:   "celestia keyring path",
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
		Value:   false,
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
		Value:   "mocha",
		EnvVars: prefixEnvVars("CELESTIA_P2P_NETWORK"),
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
	ListenAddrFlag,
	PortFlag,
}

var optionalFlags = []cli.Flag{
	GenericCommFlag,
	CelestiaBridgeAddrFlag,
	CelestiaBridgeDATLSEnabledFlag,
	CelestiaBridgeAuthTokenFlag,
	CelestiaNamespaceFlag,
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
}

func init() {
	optionalFlags = append(optionalFlags, oplog.CLIFlags(EnvVarPrefix)...)
	Flags = append(requiredFlags, optionalFlags...)
}

// Flags contains the list of configuration options available to the binary.
var Flags []cli.Flag

type CLIConfig struct {
	UseGenericComm     bool
	DAAddr             string
	DATLSEnabled       bool
	DAAuthToken        string
	Namespace          string
	DefaultKeyName     string
	KeyringPath        string
	CoreGRPCAddr       string
	CoreGRPCTLSEnabled bool
	CoreGRPCAuthToken  string
	P2PNetwork         string
	S3Config           s3.S3Config
	Fallback           bool
	Cache              bool
}

func ReadCLIConfig(ctx *cli.Context) CLIConfig {
	return CLIConfig{
		UseGenericComm:     ctx.Bool(GenericCommFlagName),
		DAAddr:             ctx.String(CelestiaBridgeAddrFlagName),
		DATLSEnabled:       ctx.Bool(CelestiaBridgeDATLSEnabledFlagName),
		DAAuthToken:        ctx.String(CelestiaBridgeAuthTokenFlagName),
		Namespace:          ctx.String(CelestiaNamespaceFlagName),
		DefaultKeyName:     ctx.String(CelestiaDefaultKeyNameFlagName),
		KeyringPath:        ctx.String(CelestiaKeyringPathFlagName),
		CoreGRPCAddr:       ctx.String(CelestiaCoreGRPCAddrFlagName),
		CoreGRPCTLSEnabled: ctx.Bool(CelestiaCoreGRPCTLSEnabledFlagName),
		CoreGRPCAuthToken:  ctx.String(CelestiaCoreGRPCAuthTokenFlagName),
		P2PNetwork:         ctx.String(CelestiaP2PNetworkFlagName),
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
	if c.CelestiaEnabled() && (c.DAAddr == "" || c.Namespace == "") {
		return errors.New("all Celestia flags must be set")
	}
	if c.CelestiaEnabled() {
		if _, err := hex.DecodeString(c.Namespace); err != nil {
			return err
		}
	}
	return nil
}

func (c CLIConfig) CelestiaConfig() celestia.CelestiaConfig {
	ns, _ := hex.DecodeString(c.Namespace)
	return celestia.CelestiaConfig{
		DAAddr:             c.DAAddr,
		DATLSEnabled:       c.DATLSEnabled,
		DAAuthToken:        c.DAAuthToken,
		Namespace:          ns,
		DefaultKeyName:     c.DefaultKeyName,
		KeyringPath:        c.KeyringPath,
		CoreGRPCAddr:       c.CoreGRPCAddr,
		CoreGRPCTLSEnabled: c.CoreGRPCTLSEnabled,
		CoreGRPCAuthToken:  c.CoreGRPCAuthToken,
		P2PNetwork:         c.P2PNetwork,
	}
}

func (c CLIConfig) CelestiaEnabled() bool {
	return !(c.DAAddr == "" && c.DAAuthToken == "" && c.Namespace == "")
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
