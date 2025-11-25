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
)

var Version = "v0.0.1"

func main() {
	oplog.SetupDefaults()

	// Load TOML config BEFORE creating the CLI app so env vars are set before flag parsing
	// Check if --config flag is in os.Args
	configFile := ""
	for i, arg := range os.Args {
		if arg == "--config" && i+1 < len(os.Args) {
			configFile = os.Args[i+1]
			break
		} else if len(arg) > 9 && arg[:9] == "--config=" {
			configFile = arg[9:]
			break
		}
	}

	if configFile != "" {
		// Load TOML and set env vars before CLI app processes flags
		tomlCfg, err := LoadConfig(configFile)
		if err != nil {
			log.Crit("Failed to load TOML config", "file", configFile, "error", err)
		}
		if err := tomlCfg.Validate(); err != nil {
			log.Crit("Invalid TOML config", "file", configFile, "error", err)
		}

		// Set environment variables from TOML
		envVars := tomlCfg.ConvertToEnvVars()
		for key, value := range envVars {
			os.Setenv(key, value)
		}
	}

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
		WorkerMaxBlobWaitTimeFlag,
		WorkerReconcilePeriodFlag,
		WorkerReconcileAgeFlag,
		WorkerGetTimeoutFlag,
		ReadOnlyFlag,
		TrustedSignerFlag,
		BackfillEnabledFlag,
		BackfillStartHeightFlag,
		BackfillPeriodFlag,
	))
	app.Version = opservice.FormatVersion(Version, "", "", "")
	app.Name = "da-server"
	app.Usage = "Alt-DA Storage Service"
	app.Description = "Service for storing alt-da inputs to Celestia with async batching"
	app.Action = StartDAServer

	ctx := ctxinterrupt.WithSignalWaiterMain(context.Background())
	err := app.RunContext(ctx, os.Args)
	if err != nil {
		log.Crit("Application failed", "message", err)
	}

}
