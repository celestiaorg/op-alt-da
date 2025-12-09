package main

import (
	"context"
	"fmt"
	"time"

	"github.com/urfave/cli/v2"

	celestia "github.com/celestiaorg/op-alt-da"
	"github.com/celestiaorg/op-alt-da/fallback"
	"github.com/celestiaorg/op-alt-da/fallback/s3"
	"github.com/ethereum-optimism/optimism/op-service/ctxinterrupt"
	oplog "github.com/ethereum-optimism/optimism/op-service/log"
)

type Server interface {
	Start(ctx context.Context) error
	Stop() error
}

// firstNonEmpty returns the first non-empty string from the arguments.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func StartDAServer(cliCtx *cli.Context) error {
	if err := CheckRequired(cliCtx); err != nil {
		return err
	}

	logCfg := oplog.ReadCLIConfig(cliCtx)
	l := oplog.NewLogger(oplog.AppOut(cliCtx), logCfg)
	oplog.SetGlobalLogHandler(l.Handler())

	l.Info("Initializing Stateless Alt-DA server...")

	// Build config from CLI flags and/or TOML file
	cfg, err := BuildConfigFromCLI(cliCtx)
	if err != nil {
		return fmt.Errorf("failed to build config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	// Parse Celestia operation timeouts
	submitTimeout, err := cfg.GetSubmissionTimeout()
	if err != nil {
		return fmt.Errorf("invalid submission timeout: %w", err)
	}

	getTimeout, err := cfg.GetReadTimeout()
	if err != nil {
		return fmt.Errorf("invalid read timeout: %w", err)
	}

	// Parse HTTP server timeouts
	httpReadTimeout, err := cfg.GetHTTPReadTimeout()
	if err != nil {
		return fmt.Errorf("invalid http read timeout: %w", err)
	}

	httpWriteTimeout, err := cfg.GetHTTPWriteTimeout()
	if err != nil {
		return fmt.Errorf("invalid http write timeout: %w", err)
	}

	httpIdleTimeout, err := cfg.GetHTTPIdleTimeout()
	if err != nil {
		return fmt.Errorf("invalid http idle timeout: %w", err)
	}

	maxBlobSize, err := cfg.GetMaxBlobSize()
	if err != nil {
		return fmt.Errorf("invalid max blob size: %w", err)
	}

	// For backward compatibility, also check CLI-only config
	cliCfg := ReadCLIConfig(cliCtx)

	var server Server

	// Determine which config to use for Celestia settings
	celestiaRPCConfig := cfg.ToCelestiaRPCConfig()
	if celestiaRPCConfig.URL == "" && cliCfg.CelestiaRPCClientEnabled() {
		// Fall back to CLI config if TOML didn't provide celestia settings
		celestiaRPCConfig = cliCfg.CelestiaConfig()
	}

	if celestiaRPCConfig.URL != "" || len(celestiaRPCConfig.Namespace) > 0 {
		l.Info("Using celestia storage", "url", celestiaRPCConfig.URL)
		store, err := celestia.NewCelestiaStore(cliCtx.Context, celestiaRPCConfig)
		if err != nil {
			return fmt.Errorf("failed to create celestia store: %w", err)
		}

		// Use config values, with CLI flags as fallback
		addr := cfg.Addr
		if addr == "" {
			addr = cliCtx.String(ListenAddrFlagName)
		}
		port := cfg.Port
		if port == 0 {
			port = cliCtx.Int(PortFlagName)
		}

		// Default timeouts if not configured
		if submitTimeout == 0 {
			submitTimeout = 60 * time.Second
		}
		if getTimeout == 0 {
			getTimeout = 30 * time.Second
		}

		// Initialize fallback provider (prefer TOML config, fall back to CLI flags)
		var fallbackProvider fallback.Provider = &fallback.NoopProvider{}
		fallbackEnabled := cfg.Fallback.Enabled || cliCtx.Bool(FallbackEnabledFlagName)
		if fallbackEnabled {
			provider := cfg.Fallback.Provider
			if provider == "" {
				provider = cliCtx.String(FallbackProviderFlagName)
			}

			switch provider {
			case "s3":
				// Build S3 config from TOML, with CLI flags as fallback
				s3Timeout, _ := cfg.GetS3Timeout()
				if cliCtx.IsSet(FallbackS3TimeoutFlagName) {
					s3Timeout = cliCtx.Duration(FallbackS3TimeoutFlagName)
				}

				s3Cfg := s3.Config{
					Bucket:          firstNonEmpty(cfg.Fallback.S3.Bucket, cliCtx.String(FallbackS3BucketFlagName)),
					Prefix:          firstNonEmpty(cfg.Fallback.S3.Prefix, cliCtx.String(FallbackS3PrefixFlagName)),
					Endpoint:        firstNonEmpty(cfg.Fallback.S3.Endpoint, cliCtx.String(FallbackS3EndpointFlagName)),
					Region:          firstNonEmpty(cfg.Fallback.S3.Region, cliCtx.String(FallbackS3RegionFlagName)),
					CredentialType:  firstNonEmpty(cfg.Fallback.S3.CredentialType, cliCtx.String(FallbackS3CredTypeFlagName)),
					AccessKeyID:     firstNonEmpty(cfg.Fallback.S3.AccessKeyID, cliCtx.String(FallbackS3AccessKeyFlagName)),
					AccessKeySecret: firstNonEmpty(cfg.Fallback.S3.AccessKeySecret, cliCtx.String(FallbackS3SecretKeyFlagName)),
					Timeout:         s3Timeout,
				}

				if s3Cfg.Bucket == "" {
					return fmt.Errorf("fallback.s3.bucket is required when fallback is enabled with s3 provider")
				}

				s3Provider, err := s3.NewS3Provider(cliCtx.Context, s3Cfg)
				if err != nil {
					return fmt.Errorf("failed to initialize S3 fallback provider: %w", err)
				}
				fallbackProvider = s3Provider
				l.Info("Fallback provider initialized",
					"provider", "s3",
					"bucket", s3Cfg.Bucket,
					"prefix", s3Cfg.Prefix)
			default:
				return fmt.Errorf("unknown fallback provider: %s", provider)
			}
		}

		server = celestia.NewCelestiaServer(
			addr,
			port,
			store,
			submitTimeout,
			getTimeout,
			httpReadTimeout,
			httpWriteTimeout,
			httpIdleTimeout,
			maxBlobSize,
			cfg.Metrics.Enabled,
			cfg.Metrics.Port,
			fallbackProvider,
			l,
		)
	} else {
		return fmt.Errorf("celestia configuration is required")
	}

	if err := server.Start(cliCtx.Context); err != nil {
		return fmt.Errorf("failed to start the DA server: %w", err)
	}
	l.Info("Started DA Server")

	defer func() {
		if err := server.Stop(); err != nil {
			l.Error("failed to stop DA server", "err", err)
		}
	}()

	ctxinterrupt.Wait(cliCtx.Context)

	return nil
}
