package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v2"

	celestia "github.com/celestiaorg/op-alt-da"
	oplog "github.com/ethereum-optimism/optimism/op-service/log"
)

const (
	InitOutputFileFlagName = "output"
	InitOutputFormatFlagName = "format"
)

var (
	InitOutputFileFlag = &cli.StringFlag{
		Name:    InitOutputFileFlagName,
		Usage:   "output file path for signer addresses (default: stdout)",
		Value:   "",
		EnvVars: prefixEnvVars("INIT_OUTPUT"),
	}
	InitOutputFormatFlag = &cli.StringFlag{
		Name:    InitOutputFormatFlagName,
		Usage:   "output format: text, json, or toml (for trusted_signers array)",
		Value:   "text",
		EnvVars: prefixEnvVars("INIT_FORMAT"),
	}
)

// InitCommand returns the CLI command for initializing the keyring and printing signer addresses
func InitCommand() *cli.Command {
	return &cli.Command{
		Name:  "init",
		Usage: "Initialize keyring and print all signer addresses for trusted_signers configuration",
		Description: `Initialize the keyring with the primary key and any worker keys (if tx_worker_accounts > 1).
This command outputs all signer addresses that must be added to the reader's trusted_signers configuration.

Example workflow:
  1. Configure your writer's config.toml with tx_worker_accounts
  2. Run: da-server init --config config.toml
  3. Copy the output addresses to your reader's trusted_signers configuration
  4. Start the writer server
  5. Start the reader server with the trusted_signers configured

Note: Worker keys are created in the keyring when tx_worker_accounts > 1.
All addresses (primary + workers) must be in trusted_signers for security.`,
		Flags: []cli.Flag{
			ConfigFileFlag,
			// Celestia config flags for CLI mode
			CelestiaKeyringPathFlag,
			CelestiaDefaultKeyNameFlag,
			CelestiaTxWorkerAccountsFlag,
			// Output options
			InitOutputFileFlag,
			InitOutputFormatFlag,
		},
		Action: runInit,
	}
}

func runInit(cliCtx *cli.Context) error {
	// Initialize logger
	logCfg := oplog.ReadCLIConfig(cliCtx)
	l := oplog.NewLogger(oplog.AppOut(cliCtx), logCfg)

	// Load configuration
	var cfg celestia.CelestiaClientConfig
	configFile := cliCtx.String(ConfigFileFlagName)

	if configFile != "" {
		l.Info("Loading configuration from TOML file", "path", configFile)
		tomlCfg, err := LoadConfig(configFile)
		if err != nil {
			return fmt.Errorf("failed to load config file '%s': %w", configFile, err)
		}

		cfg = celestia.CelestiaClientConfig{
			KeyringPath:      tomlCfg.Celestia.KeyringPath,
			DefaultKeyName:   tomlCfg.Celestia.DefaultKeyName,
			TxWorkerAccounts: tomlCfg.Celestia.TxWorkerAccounts,
		}
	} else {
		// Use CLI flags
		cfg = celestia.CelestiaClientConfig{
			KeyringPath:      cliCtx.String(CelestiaKeyringPathFlagName),
			DefaultKeyName:   cliCtx.String(CelestiaDefaultKeyNameFlagName),
			TxWorkerAccounts: cliCtx.Int(CelestiaTxWorkerAccountsFlagName),
		}
	}

	// Validate required fields
	if cfg.KeyringPath == "" {
		return fmt.Errorf("keyring path is required (--celestia.keyring-path or celestia.keyring_path in TOML)")
	}
	if cfg.DefaultKeyName == "" {
		cfg.DefaultKeyName = "my_celes_key"
	}

	// Ensure keyring directory exists
	if err := os.MkdirAll(cfg.KeyringPath, 0755); err != nil {
		return fmt.Errorf("failed to create keyring directory: %w", err)
	}

	l.Info("Initializing signer addresses",
		"keyring_path", cfg.KeyringPath,
		"key_name", cfg.DefaultKeyName,
		"tx_worker_accounts", cfg.TxWorkerAccounts)

	// Initialize keyring and get all addresses
	addresses, err := celestia.InitializeSignerAddresses(cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize signer addresses: %w", err)
	}

	l.Info("Signer addresses initialized successfully",
		"primary", addresses.Primary,
		"worker_count", len(addresses.Workers))

	// Format output
	outputFormat := cliCtx.String(InitOutputFormatFlagName)
	outputFile := cliCtx.String(InitOutputFileFlagName)

	var output string
	switch outputFormat {
	case "json":
		output, err = formatJSON(addresses)
	case "toml":
		output, err = formatTOML(addresses)
	default:
		output, err = formatText(addresses)
	}
	if err != nil {
		return fmt.Errorf("failed to format output: %w", err)
	}

	// Write output
	if outputFile != "" {
		// Ensure output directory exists
		dir := filepath.Dir(outputFile)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("failed to create output directory: %w", err)
			}
		}
		if err := os.WriteFile(outputFile, []byte(output), 0644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		l.Info("Signer addresses written to file", "path", outputFile)
	} else {
		// Print to stdout
		fmt.Println(output)
	}

	// Print usage instructions
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "=== IMPORTANT ===")
	fmt.Fprintln(os.Stderr, "Add ALL the above addresses to your reader's trusted_signers configuration.")
	fmt.Fprintln(os.Stderr, "Without this, readers will reject blobs from your writer (security feature).")
	if cfg.TxWorkerAccounts > 1 {
		fmt.Fprintf(os.Stderr, "\nNote: With tx_worker_accounts=%d, you have %d worker accounts.\n", cfg.TxWorkerAccounts, len(addresses.Workers))
		fmt.Fprintln(os.Stderr, "All worker addresses must be in trusted_signers.")
	}

	return nil
}

func formatText(addresses *celestia.SignerAddresses) (string, error) {
	var output string
	output += "=== Signer Addresses for trusted_signers ===\n\n"
	output += fmt.Sprintf("Primary: %s\n", addresses.Primary)
	if len(addresses.Workers) > 0 {
		output += "\nWorkers:\n"
		for i, addr := range addresses.Workers {
			output += fmt.Sprintf("  [%d] %s\n", i, addr)
		}
	}
	output += "\n=== TOML Format ===\n"
	output += "trusted_signers = [\n"
	output += fmt.Sprintf("  \"%s\",\n", addresses.Primary)
	for _, addr := range addresses.Workers {
		output += fmt.Sprintf("  \"%s\",\n", addr)
	}
	output += "]\n"
	return output, nil
}

func formatJSON(addresses *celestia.SignerAddresses) (string, error) {
	type jsonOutput struct {
		Primary        string   `json:"primary"`
		Workers        []string `json:"workers,omitempty"`
		TrustedSigners []string `json:"trusted_signers"`
	}
	out := jsonOutput{
		Primary:        addresses.Primary,
		Workers:        addresses.Workers,
		TrustedSigners: addresses.AllAddresses(),
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func formatTOML(addresses *celestia.SignerAddresses) (string, error) {
	var output string
	output += "# Add this to your reader's config.toml under [worker]\n"
	output += "trusted_signers = [\n"
	output += fmt.Sprintf("  \"%s\",\n", addresses.Primary)
	for _, addr := range addresses.Workers {
		output += fmt.Sprintf("  \"%s\",\n", addr)
	}
	output += "]\n"
	return output, nil
}

