# Setup and Testing Guide

This guide is intentionally based on the standard Optimism operator docs first,
then adds only the Celestia `op-alt-da` changes specific to this repository.

## Canonical References

Use these as the baseline:

- Full standard tutorial:
  https://docs.optimism.io/chain-operators/tutorials/create-l2-rollup/create-l2-rollup
- `op-deployer` setup tutorial:
  https://docs.optimism.io/operators/chain-operators/tutorials/create-l2-rollup/op-deployer-setup
- Sequencer (`op-geth` + `op-node`) tutorial:
  https://docs.optimism.io/chain-operators/tutorials/create-l2-rollup/op-geth-setup
- Batcher tutorial:
  https://docs.optimism.io/chain-operators/tutorials/create-l2-rollup/op-batcher-setup
- Proposer tutorial:
  https://docs.optimism.io/chain-operators/tutorials/create-l2-rollup/op-proposer-setup
- Alt-DA mode explainer:
  https://docs.optimism.io/op-stack/features/experimental/alt-da-mode
- Alt-DA feature/config reference:
  https://docs.optimism.io/operators/chain-operators/features/alt-da-mode
- `op-node` Alt-DA flags reference:
  https://docs.optimism.io/node-operators/reference/op-node-config

This repo provides the Celestia DA server that plugs into that standard OP
Stack flow.

## Recommended Workspace

```text
work/
├── optimism
├── op-geth
└── op-alt-da
```

Clone the repos you need:

```bash
git clone https://github.com/ethereum-optimism/optimism
git clone https://github.com/ethereum-optimism/op-geth
git clone https://github.com/celestiaorg/op-alt-da
```

If you are working from this checkout already, use it as the `op-alt-da` repo.

## Base Flow From Standard Optimism Docs

Follow the official Optimism docs for the standard OP Stack setup:

1. Build and install `op-deployer`.
2. Create the deployment workdir and generate an intent.
3. Apply the intent to deploy L1 contracts.
4. Generate the L2 genesis and rollup config.
5. Initialize `op-geth` with the generated genesis.
6. Start L1, `op-geth`, `op-node`, `op-batcher`, and `op-proposer`.

Do not replace that baseline with a custom local script flow unless you
specifically need to. This document only covers what changes when Celestia
`op-alt-da` is the DA layer.

## Docker Compose Option

If you want to bring up a hard-coded image set instead of starting each service
manually, this compose file is a reasonable starting point:

- https://github.com/nuke-web3/ethereum-docker/blob/master/docker-compose.optimism.yml

For this repo, that compose path still needs two extra steps before it is
useful for Alt-DA testing:

1. Build the local `op-alt-da` image that the stack should run.
2. Run the full `just genesis` flow so the L1, Celestia, and OP Stack
   configuration and genesis artifacts exist before `docker compose up`.

Build the image from this repo:

```bash
docker build -t op-alt-da:local .
```

Then update the compose setup to use that image tag for the DA server
container.

Treat `just genesis` as required initialization, not an optional shortcut. The
compose stack should consume the generated artifacts; it should not replace the
full genesis flow that prepares L1 contracts, Celestia wiring, and the OP Stack
chain configuration.

## What Changes For Celestia `op-alt-da`

The standard Optimism docs assume normal Ethereum DA. For this repository, make
the following changes.

### 1. Rollup Config: Enable Alt-DA

Use the new `alt_da` runtime config shape described in the Optimism Alt-DA
docs, not the legacy `plasma` fields.

The important values are:

- `da_commitment_type = "GenericCommitment"`
- `da_challenge_window`
- `da_resolve_window`
- `da_challenge_contract_address` if your deployment uses one

This repo is the DA server that produces and serves the generic commitments.

### 2. `op-node`: Point to the DA Server

In addition to the standard `op-node` configuration, set:

```text
--altda.enabled=true
--altda.da-server=http://127.0.0.1:3100
--altda.verify-on-read=true
```

Use the current `--altda.*` flags, not the older `--plasma.*` names.

### 3. `op-batcher`: Use DA Service Mode

In addition to the standard `op-batcher` configuration, set:

```text
--altda.enabled=true
--altda.da-service=true
--altda.da-server=http://127.0.0.1:3100
```

The point of `da-service` mode is that this DA server generates the commitment
and stores the preimage on Celestia.

### 4. `op-geth` and `op-proposer`

Per the Optimism Alt-DA docs, no Alt-DA-specific changes are required for:

- `op-geth`
- `op-proposer`

### 5. Run the Celestia DA Server From This Repo

Build the server:

```bash
go test ./...
make da-server
```

Then start it with the Celestia settings that match your environment.

QuickNode-style example:

```bash
./bin/da-server \
  --addr=127.0.0.1 \
  --port=3100 \
  --celestia.namespace=<YOUR_NAMESPACE> \
  --celestia.server=<YOUR_CELESTIA_RPC_URL> \
  --celestia.tls-enabled \
  --celestia.tx-client.core-grpc.addr=<YOUR_CELESTIA_CORE_GRPC_ADDR> \
  --celestia.tx-client.core-grpc.tls-enabled \
  --celestia.tx-client.keyring-path=<YOUR_KEYRING_PATH> \
  --celestia.tx-client.key-name=<YOUR_KEY_NAME> \
  --celestia.tx-client.p2p-network=<YOUR_CELESTIA_NETWORK> \
  --metrics.enabled \
  --metrics.port=6060
```

Local light-node style example:

```bash
./bin/da-server \
  --addr=127.0.0.1 \
  --port=3100 \
  --celestia.namespace=<YOUR_NAMESPACE> \
  --celestia.server=http://127.0.0.1:26658 \
  --celestia.auth-token=<YOUR_LIGHT_NODE_TOKEN> \
  --celestia.tx-client.core-grpc.addr=<YOUR_CORE_GRPC_ADDR> \
  --celestia.tx-client.core-grpc.tls-enabled \
  --celestia.tx-client.keyring-path=<YOUR_KEYRING_PATH> \
  --celestia.tx-client.key-name=<YOUR_KEY_NAME> \
  --celestia.tx-client.p2p-network=<YOUR_CELESTIA_NETWORK> \
  --metrics.enabled \
  --metrics.port=6060
```

The authoritative repo-local configuration reference remains:

- `README.md`
- `config.toml.example`
- `cmd/daserver/config.go`
- `cmd/daserver/flags.go`

## Suggested End-to-End Order

Using the standard Optimism operator flow, the usual order is:

1. Prepare L1 and deploy contracts with `op-deployer`.
2. Generate the rollup config with Alt-DA enabled.
3. Run the full `just genesis` flow if you are using a generated local devnet.
4. Initialize and start `op-geth`.
5. Build the local `op-alt-da` image if the DA server will run via Docker.
6. Start this repo’s `da-server`.
7. Start `op-node` with `--altda.enabled=true`.
8. Start `op-batcher` with `--altda.enabled=true --altda.da-service=true`.
9. Start `op-proposer`.

That keeps the setup close to the official Optimism docs while inserting the
Celestia DA server only where Alt-DA requires it.

## Manual Validation Checklist

### Repo-local checks

```bash
go test ./...
```

### DA server checks

Health:

```bash
curl -i http://127.0.0.1:3100/health
```

Store bytes using the spec-style `POST /put`:

```bash
COMMITMENT=$(curl -sS -X POST http://127.0.0.1:3100/put \
  -H 'Content-Type: application/octet-stream' \
  --data-binary 'hello alt-da')

echo "$COMMITMENT"
```

Read them back:

```bash
curl -sS "http://127.0.0.1:3100/get/${COMMITMENT}"
```

Expected result:

- `POST /put` returns a commitment
- `GET /get/<commitment>` returns the original bytes

### OP Stack checks

After `op-node` and `op-batcher` are up:

- check `optimism_syncStatus`
- confirm L2 blocks are advancing
- send a test L2 transaction
- confirm batcher logs show Alt-DA activity
- confirm the DA server logs show blob submission and later retrieval

Useful checks:

```bash
curl -sS -X POST http://127.0.0.1:<OP_NODE_RPC_PORT> \
  -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","method":"optimism_syncStatus","params":[],"id":1}'
```

```bash
cast block-number --rpc-url http://127.0.0.1:<OP_GETH_HTTP_PORT>
```

```bash
cast send \
  --rpc-url http://127.0.0.1:<OP_GETH_HTTP_PORT> \
  --private-key <YOUR_TEST_KEY> \
  --value 0.001ether \
  <RECIPIENT_ADDRESS>
```

## What To Watch For

When adapting the standard Optimism flow to Celestia `op-alt-da`, the most
common problems are:

- using legacy `plasma` names instead of `altda`
- forgetting `--altda.da-service=true` on `op-batcher`
- pointing `op-node` and `op-batcher` at different DA server URLs
- starting `op-node` or `op-batcher` before the DA server is reachable
- mismatching the Celestia namespace between the DA server and the chain config
- testing the OP Stack against a prebuilt `op-alt-da` binary that does not
  match the local source tree

## What To Record In Reviews

When using this guide to validate a change, record:

- which official Optimism docs were followed
- the exact Alt-DA-specific config changes applied
- whether `go test ./...` passed in this repo
- the exact `da-server`, `op-node`, and `op-batcher` flags used
- whether manual `POST /put` and `GET /get` checks passed
- whether L2 blocks advanced and a test transaction landed
- any mismatch between observed behavior and the Optimism Alt-DA docs/spec
