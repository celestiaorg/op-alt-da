# AGENTS.md

This repository implements the Celestia-backed DA server for the OP Stack.

## Specification

Treat the [Optimism Alt-DA specification](https://specs.optimism.io/experimental/alt-da.html)
as the source of truth. Read it before reviewing or changing behavior and
check the DA Server section and commitment rules. If sources disagree,
prioritize the spec, then the implementation, then local documentation.

This repo owns the DA server, Celestia storage/commitment encoding, config,
signers, and fallback storage. The upstream derivation pipeline, challenge
contracts, fault proof preimage oracle, and L1 deployment logic are out of scope.

## Contract checks

- `POST /put` stores bytes and returns a commitment.
- `POST /put/<hex_encoded_commitment>` exists if precomputed commitments are supported.
- `GET /get/<hex_encoded_commitment>` returns the original preimage bytes.
- Storage failures return errors so the batcher can retry.
- Emitted commitments can be parsed and are sufficient for later retrieval;
  retrieved data matches the commitment.
- Not-found and internal-error behavior remain stable and documented.
- Document extensions beyond the spec as extensions.

Also check Celestia byte handling, generic commitment encoding, compact vs full
blob IDs, config defaults/required flags, signer modes/secret handling, and
fallback storage against the DA server contract.

## Testing and reporting

- Run `go test ./...` for code changes.
- For changes to DA server behavior, commitments, startup/config, or OP Stack
  integration, run the `op-celestia-devnet` end-to-end flow in
  [SETUP_TESTING.md](SETUP_TESTING.md).
- For behavior reviews and changes, report the spec clauses and implementing
  files checked, mismatches or ambiguities, tests/scripts/checks run, and gaps
  requiring upstream Optimism validation.
