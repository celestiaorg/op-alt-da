# Trusted Signers: Bech32 Format Update

## Summary

Updated trusted signers configuration to use **Bech32 format** (e.g., `celestia15m7s9d0...`) instead of hex format, using official constants from `celestia-app/v6/app/params`.

## Changes

### 1. Configuration Format

**Before:**
```toml
[worker]
trusted_signers = ["a1b2c3d4e5f6789012345678901234567890abcd"]  # Hex format
```

**After:**
```toml
[worker]
trusted_signers = ["celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc"]  # Bech32 format
```

### 2. Log Output

**Before:**
```
INFO Celestia signer address configured    signer_hex=a1b2c3d4e5f6789012345678901234567890abcd
```

**After:**
```
INFO Celestia signer address configured    signer_bech32=celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc    signer_hex=a1b2c3d4e5f6789012345678901234567890abcd
```

### 3. Code Changes

#### Using celestia-app Constants

**`sdkconfig/sdkconfig.go`** - Initialize SDK with official constants:
```go
import "github.com/celestiaorg/celestia-app/v6/app/params"

config.SetBech32PrefixForAccount(params.Bech32PrefixAccAddr, params.Bech32PrefixAccPub)
// params.Bech32PrefixAccAddr = "celestia" (from celestia-app)
```

**`cmd/daserver/config.go`** - Validate using official prefix:
```go
if !strings.HasPrefix(signer, params.Bech32PrefixAccAddr) {
    return fmt.Errorf("signer[%d] must be a Celestia Bech32 address starting with %q", ...)
}
```

#### Signer Comparison

**`worker/backfill_worker.go`** - Compare Bech32 to Bech32:
```go
// Convert blob signer (raw bytes) to Bech32 address for comparison
blobSignerAddr := sdk.AccAddress(blobSigner)
blobSignerBech32 := blobSignerAddr.String()

for _, trustedSigner := range w.workerCfg.TrustedSigners {
    if blobSignerBech32 == trustedSigner {
        // Match!
    }
}
```

#### Logging

**`celestia_storage.go`** - Log both formats:
```go
signerBech32 := sdk.AccAddress(signerAddr).String()
logger.Info("Celestia signer address configured",
    "signer_bech32", signerBech32,
    "signer_hex", hex.EncodeToString(signerAddr))
```

### 4. Documentation Updates

- **README.md** - Updated examples to use Bech32 format
- **config.toml** - Updated comments and examples
- **config.toml.example** - Updated comments and examples

## Benefits

✅ **User-friendly** - Bech32 addresses are what users see in wallets and CLI tools
✅ **No manual conversion** - Copy-paste directly from celestia-appd/wallets
✅ **Official constants** - Uses `celestia-app/v6/app/params.Bech32PrefixAccAddr`
✅ **Backwards compatible** - Validation rejects old hex format with clear error
✅ **Future-proof** - Automatically updates if Celestia changes the prefix

## Migration Guide

### For New Deployments

Simply use Bech32 addresses from your wallet:
```toml
[worker]
trusted_signers = ["celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc"]
```

### For Existing Deployments

If you have hex format in your config, the server will reject it with a clear error:
```
Error: invalid worker.trusted_signers: signer[0] must be a Celestia Bech32 address starting with "celestia", got: "a1b2c3d4..."
```

**To convert:**
1. Check your write server logs for the new Bech32 format:
   ```bash
   grep "signer_bech32" /var/log/celestia-da.log
   ```

2. Or convert manually using celestia-appd:
   ```bash
   # If you have the hex: a1b2c3d4e5f6789012345678901234567890abcd
   # Use celestia-appd to convert (or decode using cosmos-sdk)
   ```

3. Update config.toml with the Bech32 address

4. Restart server

## Testing

All tests pass with Bech32 format:
- ✅ Config validation tests
- ✅ Worker signer verification tests
- ✅ Full unit test suite

## Examples

### Write Server Setup
```bash
# 1. Start write server
./bin/da-server --config config.toml

# 2. Get signer address from logs
grep "Celestia signer address configured" /var/log/celestia-da.log
# Output: signer_bech32=celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc
```

### Read-Only Server Setup
```toml
# config-readonly.toml
read_only = true

[worker]
trusted_signers = [
  "celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc"  # From write server logs
]

[backfill]
enabled = true
start_height = 8963369
```

```bash
./bin/da-server --config config-readonly.toml
# No more "No trusted signers configured" warning!
```
