package main

import (
	"context"
	"os"

	"github.com/ethereum/go-ethereum/log"
	"github.com/urfave/cli/v2"

	opservice "github.com/ethereum-optimism/optimism/op-service"
	"github.com/ethereum-optimism/optimism/op-service/cliapp"
	"github.com/ethereum-optimism/optimism/op-service/ctxinterrupt"
	oplog "github.com/ethereum-optimism/optimism/op-service/log"

	"github.com/celestiaorg/op-alt-da/sdkconfig"
)

var Version = "v0.0.1"

func main() {
	oplog.SetupDefaults()

	// Configure Cosmos SDK for Celestia Bech32 addresses
	sdkconfig.InitCelestiaPrefix()

	// Note: TOML config is now loaded directly in StartDAServer, not via env vars
	// This is more idiomatic for Go applications

	app := cli.NewApp()
	app.Flags = cliapp.ProtectFlags(append(Flags,
		DBPathFlag,
		BackupEnabledFlag,
		BackupIntervalFlag,
		BatchMinBlobsFlag,
		BatchMaxBlobsFlag,
		BatchTargetBlobsFlag,
		BatchMaxSizeFlag,
		BatchMinSizeFlag,
		WorkerSubmitPeriodFlag,
		WorkerSubmitTimeoutFlag,
		WorkerMaxRetriesFlag,
		WorkerMaxParallelSubmissionsFlag,
		WorkerMaxBlobWaitTimeFlag,
		WorkerReconcilePeriodFlag,
		WorkerReconcileAgeFlag,
		WorkerGetTimeoutFlag,
		TrustedSignersFlag,
		BackfillEnabledFlag,
		BackfillStartHeightFlag,
		BackfillTargetHeightFlag,
		BackfillPeriodFlag,
		BackfillBlocksPerScanFlag,
	))
	app.Version = opservice.FormatVersion(Version, "", "", "")
	app.Name = "da-server"
	app.Usage = "Alt-DA Storage Service"
	app.Description = "Service for storing alt-da inputs to Celestia with async batching and parallel submission"
	app.Action = StartDAServer
	app.Commands = []*cli.Command{
		InitCommand(),
	}

	ctx := ctxinterrupt.WithSignalWaiterMain(context.Background())
	err := app.RunContext(ctx, os.Args)
	if err != nil {
		log.Crit("Application failed", "message", err)
	}

}
