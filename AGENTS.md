# AGENTS.md

## Purpose

This repository implements a Celestia-backed Alt-DA server for the OP Stack.
When working in this repo, agents must treat the Optimism Alt-DA specification
as the source of truth and compare the implementation against that spec.

Canonical spec:

- https://specs.optimism.io/experimental/alt-da.html

Companion local guide for setup and end-to-end testing:

- `SETUP_TESTING.md`

## Mandatory Workflow

1. Read the Alt-DA spec before reviewing or changing behavior.
2. Identify which parts of the spec are in scope for this repo.
3. Compare code behavior to the spec, not just to README text.
4. Call out any spec mismatches explicitly in code reviews and change summaries.
5. Run repo-local tests for code changes.
6. Use the `op-celestia-devnet` flow from `SETUP_TESTING.md` for any change that
   affects DA server behavior, commitments, startup/config, or OP Stack
   integration.

If the spec, code, and docs disagree, use this priority order:

1. Alt-DA spec
2. Current implementation
3. Local documentation

## Repo Scope

This repo is primarily responsible for the DA server side of Alt-DA, not the
full upstream derivation/challenge pipeline.

Main local areas of responsibility:

- `celestia_server.go`: HTTP endpoints and request/response behavior
- `celestia_storage.go`: Celestia commitment encoding, submit/get logic, blob ID handling
- `cmd/daserver/config.go`: config model and validation
- `cmd/daserver/config_builder.go`: CLI and TOML merging
- `cmd/daserver/entrypoint.go`: wiring, startup, fallback provider initialization
- `signer/`: signer backends and validation
- `fallback/`: fallback storage behavior

Usually out of scope for this repo and owned upstream by Optimism or deployment
infrastructure:

- derivation pipeline behavior inside `op-node`
- challenge contract implementation
- fault proof preimage oracle integration
- OP Stack L1 contract deployment logic

## Spec Checklist

Agents should explicitly verify the DA server behavior against the spec section
"DA Server" and the surrounding commitment rules:

- `POST /put/<hex_encoded_commitment>` exists if precomputed commitments are
  supported by the implementation.
- `POST /put` stores bytes and returns a commitment.
- `GET /get/<hex_encoded_commitment>` returns the original preimage bytes.
- Failed storage returns an error so the batcher can retry.
- Returned commitments are sufficient for later retrieval.
- The server can parse the commitment payload it emits.
- The data returned from the DA layer matches what was committed.
- Not-found and internal-error behavior are stable and documented.
- Any extension beyond the spec is documented as an extension, not mistaken for
  normative behavior.

For this repo specifically, also verify:

- Celestia DA layer byte handling and generic commitment encoding
- compact vs full blob ID behavior
- config defaults and required flags
- signer mode behavior and secret handling
- fallback storage behavior does not silently violate DA server expectations

## Testing Expectations

Minimum for code changes:

- `go test ./...`

Required for behavior changes to the DA server or OP Stack integration:

- follow `SETUP_TESTING.md`
- run the `op-celestia-devnet` end-to-end flow
- include the exact scripts and checks run in the final report

Useful local test entrypoints:

- `celestia_server_test.go`
- `celestia_storage_test.go`
- `tests/integration/`
- `tests/manual/throughput/`

## Deliverables

When an agent reviews or changes this repo, the output should include:

- which spec clauses were checked
- which files implement those clauses
- any mismatches or ambiguities found
- tests run
- any remaining gaps that still require upstream Optimism validation
