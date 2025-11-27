# Fuzz Testing Guide

## Overview

This directory contains fuzz tests for the critical business logic in op-alt-da:
1. **Batch packing/unpacking** - Blob serialization for Celestia
2. **Commitment computation** - Deterministic blob identifiers
3. **Blob ID serialization** - Binary format for on-chain references

Fuzz testing automatically generates thousands of random inputs to find edge cases, crashes, panics, and data corruption issues that traditional tests might miss.

## What is Fuzz Testing?

Fuzz testing (fuzzing) is an automated testing technique that:
- **Generates** random/semi-random inputs automatically
- **Mutates** seed inputs to explore edge cases
- **Discovers** crashes, panics, hangs, and assertion failures
- **Minimizes** failing inputs to smallest reproducible case
- **Saves** interesting inputs to corpus for regression testing

### Traditional Test vs Fuzz Test

**Traditional Test:**
```go
func TestBatchPack(t *testing.T) {
    data := []byte("test data")  // YOU choose this
    // Test with this one input
}
```

**Fuzz Test:**
```go
func FuzzBatchPack(f *testing.F) {
    f.Add([]byte("seed"))  // Initial seed
    f.Fuzz(func(t *testing.T, data []byte) {
        // Go automatically generates THOUSANDS of inputs:
        // []byte{}, []byte{0}, []byte{255}, 
        // []byte{0,255,0,255}, random bytes, etc.
    })
}
```

## Fuzz Tests Included

### 1. Batch Packing/Unpacking (`batch_fuzz_test.go`)

**7 fuzz tests covering:**

- `FuzzBatchPackUnpack` - Roundtrip integrity for any input
- `FuzzBatchUnpackCorrupted` - Graceful handling of corrupted data
- `FuzzBatchBinaryFormat` - Binary format edge cases
- `FuzzBatchSizeCalculations` - Size accounting and overflow protection
- `FuzzBatchConcurrentAccess` - Thread safety
- `FuzzBatchEmptyAndBoundary` - Empty and boundary conditions
- `FuzzBatchSizeCalculations` - Integer overflow/underflow

**What they catch:**
- Data corruption during pack/unpack
- Buffer overflows/underflows
- Integer overflow in size calculations
- Infinite loops in parsing
- Panics from malformed data
- Race conditions in concurrent access
- Off-by-one errors

### 2. Commitment Computation (`commitment_fuzz_test.go`)

**10 fuzz tests covering:**

- `FuzzCommitmentDeterminism` - Same input = same output (always)
- `FuzzCommitmentUniqueness` - Different input = different output
- `FuzzCommitmentSize` - Always produces exactly 32 bytes
- `FuzzCommitmentStability` - No hidden state or randomness
- `FuzzCommitmentBytePatterns` - Special byte patterns (0x00, 0xFF, alternating)
- `FuzzCommitmentBoundaries` - Size boundaries (0, 1, 32, 64, 1MB, etc.)
- `FuzzCommitmentConcurrent` - Thread safety
- `FuzzCommitmentMemoryCorruption` - Buffer issues

**What they catch:**
- Non-deterministic behavior
- Hash collisions (extremely unlikely but possible)
- Memory corruption
- Race conditions
- Incorrect handling of empty/large data
- Commitment size violations

### 3. Blob ID Serialization (`blobid_fuzz_test.go`)

**8 fuzz tests covering:**

- `FuzzBlobIDRoundTrip` - Marshal → Unmarshal preserves data
- `FuzzBlobIDUnmarshalCorrupted` - Graceful failure on corrupted data
- `FuzzBlobIDBackwardCompatibility` - Compact format compatibility
- `FuzzBlobIDCommitmentSize` - Commitment size validation
- `FuzzBlobIDEndianness` - Byte order correctness
- `FuzzBlobIDZeroValues` - Zero/default value handling
- `FuzzBlobIDMaxValues` - Maximum value handling

**What they catch:**
- Serialization bugs
- Endianness errors
- Buffer overflows
- Backward compatibility breaks
- Integer overflow in binary encoding

## Running Fuzz Tests

### Quick Start

```bash
# Run all fuzz tests for 30 seconds each
go test -fuzz=. -fuzztime=30s ./tests/fuzz/

# Run specific fuzz test
go test -fuzz=FuzzBatchPackUnpack -fuzztime=1m ./tests/fuzz/

# Run until failure found (or Ctrl+C)
go test -fuzz=FuzzCommitmentDeterminism ./tests/fuzz/
```

### Recommended Fuzzing Duration

**Development (quick check):**
```bash
go test -fuzz=. -fuzztime=30s ./tests/fuzz/
```

**CI Pipeline:**
```bash
go test -fuzz=. -fuzztime=2m ./tests/fuzz/
```

**Overnight/Comprehensive:**
```bash
go test -fuzz=. -fuzztime=1h ./tests/fuzz/
```

**Continuous fuzzing:**
```bash
# Run indefinitely until failure
go test -fuzz=FuzzBatchPackUnpack ./tests/fuzz/
```

### Parallel Fuzzing

```bash
# Use multiple CPU cores
go test -fuzz=. -fuzztime=5m -parallel=4 ./tests/fuzz/
```

### Controlling Iterations

```bash
# Run exact number of iterations
go test -fuzz=FuzzBatchPackUnpack -fuzztime=10000x ./tests/fuzz/
```

## Understanding Fuzz Output

### Successful Run
```
fuzz: elapsed: 0s, gathering baseline coverage: 0/20 completed
fuzz: elapsed: 3s, gathering baseline coverage: 20/20 completed, now fuzzing with 8 workers
fuzz: elapsed: 6s, execs: 2847 (949/sec), new interesting: 5 (total: 25)
fuzz: elapsed: 9s, execs: 5784 (979/sec), new interesting: 7 (total: 27)
...
PASS
```

**Key metrics:**
- `execs` - Number of test executions
- `new interesting` - Inputs that trigger new code paths (good!)
- `workers` - Parallel fuzz workers (usually = CPU cores)

### When Fuzz Test Finds a Bug
```
--- FAIL: FuzzBatchPackUnpack (3.42s)
    --- FAIL: FuzzBatchPackUnpack (0.00s)
        batch_fuzz_test.go:45: unpack failed: truncated data
    
    Failing input written to:
        testdata/fuzz/FuzzBatchPackUnpack/a8f3c2d1e4b5...
    
    To re-run:
    go test -run=FuzzBatchPackUnpack/a8f3c2d1e4b5...
```

**What happens:**
1. Go **saves** the failing input to `testdata/fuzz/`
2. You can **replay** the exact failure
3. Input is **minimized** to smallest reproducing case
4. Failure becomes a **regression test** automatically

### Replaying a Failure

```bash
# Re-run the specific failing case
go test -run=FuzzBatchPackUnpack/a8f3c2d1e4b5...

# Or run all saved corpus
go test ./tests/fuzz/
```

## Corpus Management

### What is a Corpus?

The corpus is a collection of interesting inputs discovered during fuzzing:
- Stored in `testdata/fuzz/FuzzXXX/` directories
- Automatically used in future fuzz runs
- Shared across team via git
- Acts as regression test suite

### Viewing Corpus

```bash
# See saved inputs
ls tests/fuzz/testdata/fuzz/FuzzBatchPackUnpack/

# View specific input
cat tests/fuzz/testdata/fuzz/FuzzBatchPackUnpack/a8f3c2d1e4b5...
```

### Corpus Best Practices

**DO:**
- ✅ Commit corpus to git (it's your regression test suite)
- ✅ Review new corpus entries in PRs
- ✅ Add interesting cases manually via `f.Add()`
- ✅ Keep corpus under 10MB per test

**DON'T:**
- ❌ Delete corpus without reason
- ❌ Gitignore the testdata/ directory
- ❌ Manually edit corpus files

## Seed Corpus

Each fuzz test has a **seed corpus** - initial interesting inputs:

```go
func FuzzBatchPackUnpack(f *testing.F) {
    // Seed corpus
    f.Add([]byte("single blob"), uint8(1))
    f.Add([]byte(""), uint8(1))                  // Empty
    f.Add(bytes.Repeat([]byte{0xFF}, 100), uint8(1)) // All 0xFF
    // ... more seeds
    
    f.Fuzz(func(t *testing.T, data []byte, count uint8) {
        // Fuzz function uses seeds as starting point
    })
}
```

**Good seeds:**
- Edge cases (empty, single byte, max size)
- Known problematic patterns (all zeros, all 0xFF, alternating)
- Real production data samples
- Previous bug triggers

## CI Integration

### GitHub Actions Example

```yaml
name: Fuzz Tests

on:
  push:
    branches: [main]
  pull_request:
  schedule:
    - cron: '0 0 * * *'  # Daily

jobs:
  fuzz:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      
      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.21'
      
      - name: Run fuzz tests
        run: |
          go test -fuzz=. -fuzztime=2m ./tests/fuzz/
      
      - name: Upload corpus
        if: always()
        uses: actions/upload-artifact@v3
        with:
          name: fuzz-corpus
          path: tests/fuzz/testdata/
```

### Continuous Fuzzing

For production systems, consider continuous fuzzing:

```bash
# Run overnight
nohup go test -fuzz=. -fuzztime=8h ./tests/fuzz/ &

# Or use OSS-Fuzz (Google's continuous fuzzing service)
```

## What Each Test Validates

### Batch Tests Validate:
- ✅ No data loss during pack/unpack
- ✅ Corrupted data rejected gracefully (no panics)
- ✅ Size calculations don't overflow
- ✅ Binary format parsing is robust
- ✅ Thread-safe for concurrent access
- ✅ Handles empty/max-sized blobs

### Commitment Tests Validate:
- ✅ Deterministic (same input → same output)
- ✅ Unique (different input → different output)
- ✅ Always 32 bytes
- ✅ No hidden state/randomness
- ✅ Thread-safe
- ✅ Handles any data size (0 to 1MB+)

### Blob ID Tests Validate:
- ✅ Roundtrip integrity (marshal → unmarshal)
- ✅ Backward compatibility with old formats
- ✅ Correct byte ordering (endianness)
- ✅ Handles zero/max values
- ✅ Corrupted data rejected safely

## Performance

Fuzz tests are computationally intensive:

**Expected performance:**
- ~1000-10,000 executions per second per worker
- 8 workers on 8-core machine
- ~10,000-80,000 exec/sec total

**Tuning:**
```bash
# More workers (careful: diminishing returns)
go test -fuzz=. -parallel=16 ./tests/fuzz/

# Less workers (for shared CI)
go test -fuzz=. -parallel=2 ./tests/fuzz/
```

## Interpreting Results

### "New interesting" inputs
Good! Means fuzzer found new code paths:
```
new interesting: 12 (total: 45)
```

### High execution rate
Good! Means tests are fast:
```
execs: 98234 (3274/sec)
```

### Low "new interesting" after long run
Expected - fuzzer has explored most paths:
```
fuzz: elapsed: 30s, execs: 87234 (2907/sec), new interesting: 0 (total: 45)
```

### Failure
**CRITICAL** - Found a bug! Fix it!

## Troubleshooting

### "Fuzz tests take too long"
```bash
# Reduce fuzz time
go test -fuzz=. -fuzztime=10s ./tests/fuzz/
```

### "Too much memory usage"
```bash
# Reduce parallelism
go test -fuzz=. -parallel=2 ./tests/fuzz/
```

### "Found a crash, but can't reproduce"
```bash
# Check testdata/fuzz/ for saved input
ls tests/fuzz/testdata/fuzz/FuzzXXX/

# Re-run with that specific input
go test -run=FuzzXXX/HASH
```

### "Corpus got too large"
```bash
# Keep top N most interesting inputs
go test -fuzz=. -fuzzminimizetime=1m ./tests/fuzz/
```

## Best Practices

### Writing Fuzz Tests

**DO:**
- ✅ Test properties, not exact values
- ✅ Handle errors gracefully
- ✅ Test roundtrips (serialize → deserialize)
- ✅ Add good seed corpus
- ✅ Keep tests fast (<1ms per execution)

**DON'T:**
- ❌ Skip on unexpected input (use early return)
- ❌ Test exact output values (too brittle)
- ❌ Make slow tests (network, disk I/O)
- ❌ Use time.Sleep() or external dependencies

### Good Fuzz Test Pattern

```go
func FuzzMyFunction(f *testing.F) {
    // 1. Seed corpus with interesting cases
    f.Add([]byte("normal"))
    f.Add([]byte{})
    f.Add(bytes.Repeat([]byte{0xFF}, 100))
    
    f.Fuzz(func(t *testing.T, data []byte) {
        // 2. Handle invalid input gracefully
        if len(data) > 10000 {
            t.Skip("too large for fuzzing")
        }
        
        // 3. Call function - should never panic
        result, err := MyFunction(data)
        
        // 4. Test properties, not exact values
        if err == nil {
            if len(result) == 0 && len(data) > 0 {
                t.Fatal("non-empty input produced empty output")
            }
        }
        // Errors are fine - just shouldn't panic
    })
}
```

## Further Reading

- [Go Fuzzing Tutorial](https://go.dev/doc/tutorial/fuzz)
- [Go Fuzzing Docs](https://go.dev/doc/fuzz/)
- [Fuzzing Best Practices](https://go.dev/security/fuzz/)

## Summary

These fuzz tests provide **deep testing** of your critical business logic:
- **Batch packing**: Ensures no data corruption when packing blobs for Celestia
- **Commitment computation**: Guarantees deterministic, unique 32-byte identifiers
- **Blob ID serialization**: Validates binary format correctness

Run them regularly to catch edge cases before production!

```bash
# Quick check (30s per test)
go test -fuzz=. -fuzztime=30s ./tests/fuzz/

# Daily CI run (2min per test)
go test -fuzz=. -fuzztime=2m ./tests/fuzz/

# Deep dive (1h per test)
go test -fuzz=. -fuzztime=1h ./tests/fuzz/
```


