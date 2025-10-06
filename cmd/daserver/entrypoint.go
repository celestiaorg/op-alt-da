package main

import (
	"fmt"

	"github.com/urfave/cli/v2"

	celestia "github.com/celestiaorg/op-alt-da"
	s3 "github.com/celestiaorg/op-alt-da/s3"
	"github.com/ethereum-optimism/optimism/op-service/ctxinterrupt"
	oplog "github.com/ethereum-optimism/optimism/op-service/log"
)

type Server interface {
	Start() error
	Stop() error
}

func StartDAServer(cliCtx *cli.Context) error {
	if err := CheckRequired(cliCtx); err != nil {
		return err
	}

	cfg := ReadCLIConfig(cliCtx)
	if err := cfg.Check(); err != nil {
		return err
	}

	logCfg := oplog.ReadCLIConfig(cliCtx)

	l := oplog.NewLogger(oplog.AppOut(cliCtx), logCfg)
	oplog.SetGlobalLogHandler(l.Handler())

	l.Info("Initializing Alt-DA server...")

	var server Server

	switch {
	case cfg.CelestiaRPCClientEnabled():
		l.Info("Using celestia storage", "url", cfg.CelestiaConfig().URL)
		store := celestia.NewCelestiaStore(cfg.CelestiaConfig())
		var s3Store *s3.S3Store
		var err error
		if cfg.FallbackEnabled() || cfg.CacheEnabled() {
			l.Info("Using s3 storage", "config", cfg.S3Config)
			s3Store, err = s3.NewS3(cfg.S3Config)
			if err != nil {
				return err
			}
		}
		server = celestia.NewCelestiaServer(cliCtx.String(ListenAddrFlagName), cliCtx.Int(PortFlagName), store, s3Store, cfg.FallbackEnabled(), cfg.CacheEnabled(), cfg.MetricsEnabled, cfg.MetricsPort, l)
	}

	if err := server.Start(); err != nil {
		return fmt.Errorf("failed to start the DA server: %w", err)
	} else {
		l.Info("Started DA Server")
	}

	defer func() {
		if err := server.Stop(); err != nil {
			l.Error("failed to stop DA server", "err", err)
		}
	}()

	ctxinterrupt.Wait(cliCtx.Context)

	return nil
}
