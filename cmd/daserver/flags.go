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
	ListenAddrFlagName        = "addr"
	PortFlagName              = "port"
	GenericCommFlagName       = "generic-commitment"
	CelestiaServerFlagName    = "celestia.server"
	CelestiaAuthTokenFlagName = "celestia.auth-token"
	CelestiaNamespaceFlagName = "celestia.namespace"
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
	CelestiaServerFlag = &cli.StringFlag{
		Name:    CelestiaServerFlagName,
		Usage:   "celestia server endpoint",
		Value:   "http://localhost:26658",
		EnvVars: prefixEnvVars("CELESTIA_SERVER"),
	}
	CelestiaAuthTokenFlag = &cli.StringFlag{
		Name:    CelestiaAuthTokenFlagName,
		Usage:   "celestia auth token",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_AUTH_TOKEN"),
	}
	CelestiaNamespaceFlag = &cli.StringFlag{
		Name:    CelestiaNamespaceFlagName,
		Usage:   "celestia namespace",
		Value:   "",
		EnvVars: prefixEnvVars("CELESTIA_NAMESPACE"),
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
	CelestiaServerFlag,
	CelestiaAuthTokenFlag,
	CelestiaNamespaceFlag,
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
	UseGenericComm    bool
	CelestiaEndpoint  string
	CelestiaAuthToken string
	CelestiaNamespace string
	S3Config          s3.S3Config
	Fallback          bool
	Cache             bool
}

func ReadCLIConfig(ctx *cli.Context) CLIConfig {
	return CLIConfig{
		UseGenericComm:    ctx.Bool(GenericCommFlagName),
		CelestiaEndpoint:  ctx.String(CelestiaServerFlagName),
		CelestiaAuthToken: ctx.String(CelestiaAuthTokenFlagName),
		CelestiaNamespace: ctx.String(CelestiaNamespaceFlagName),
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
		URL:       c.CelestiaEndpoint,
		AuthToken: c.CelestiaAuthToken,
		Namespace: ns,
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
