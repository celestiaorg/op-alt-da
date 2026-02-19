package main

import (
	"fmt"

	"github.com/urfave/cli/v2"

	celestia "github.com/celestiaorg/op-alt-da"
)

// KeysCommand returns the keys subcommand with its subcommands.
func KeysCommand() *cli.Command {
	return &cli.Command{
		Name:  "keys",
		Usage: "Key management commands",
		Subcommands: []*cli.Command{
			{
				Name:   "show",
				Usage:  "Show the Celestia address for the configured key",
				Flags:  []cli.Flag{ConfigFileFlag},
				Action: showKeyAddress,
			},
		},
	}
}

func showKeyAddress(cliCtx *cli.Context) error {
	configPath := cliCtx.String(ConfigFileFlagName)
	if configPath == "" {
		return fmt.Errorf("--config flag is required")
	}

	cfg, err := LoadConfigFromFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if !cfg.TxClientEnabled() {
		return fmt.Errorf("tx client is not configured (core_grpc_addr and keyring_backend required)")
	}

	rpcCfg := cfg.ToCelestiaRPCConfig()

	kr, err := celestia.InitKeyring(cliCtx.Context, &rpcCfg)
	if err != nil {
		return fmt.Errorf("failed to initialize keyring: %w", err)
	}

	keyName := cfg.Celestia.DefaultKeyName
	if keyName == "" {
		keyName = "my_celes_key"
	}

	record, err := kr.Key(keyName)
	if err != nil {
		return fmt.Errorf("failed to get key %q: %w", keyName, err)
	}

	addr, err := record.GetAddress()
	if err != nil {
		return fmt.Errorf("failed to get address from key: %w", err)
	}

	fmt.Println(addr.String())
	return nil
}
