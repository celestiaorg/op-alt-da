package main

import (
	"context"
	"os"

	"github.com/ethereum/go-ethereum/log"
	"github.com/urfave/cli/v2"

	opservice "github.com/ethereum-optimism/optimism/op-service"
	"github.com/ethereum-optimism/optimism/op-service/cliapp"
	oplog "github.com/ethereum-optimism/optimism/op-service/log"
	"github.com/ethereum-optimism/optimism/op-service/ctxinterrupt"
)

var Version = "v0.0.1"

func main() {
	oplog.SetupDefaults()

	app := cli.NewApp()
	app.Flags = cliapp.ProtectFlags(Flags)
	app.Version = opservice.FormatVersion(Version, "", "", "")
	app.Name = "da-server"
	app.Usage = "Alt-DA Storage Service"
	app.Description = "Service for storing alt-da inputs to Celestia"
	app.Action = StartDAServer

	ctx := ctxinterrupt.WithSignalWaiterMain(context.Background())
	err := app.RunContext(ctx, os.Args)
	if err != nil {
		log.Crit("Application failed", "message", err)
	}

}
