# CCSDS 121.0-B-3 Decoder Correctness & Performance Review

**Library**: `github.com/shyrmapp/aec` (pure-Go CCSDS 121.0-B-3 / AEC decoder)
**Reviewed against**: libaec v1.1.6 (C reference, [GitHub](https://github.com/Deutsches-Klimarechenzentrum/libaec))
**Date**: 2026-04-28

---

## Summary

The library is a clean, well-structured decoder that correctly handles the majority of the CCSDS 121.0-B-3 spec. All 12 bundled test vectors pass. However, **3 correctness bugs** were found that can produce incorrect (false) output on valid input, plus several significant performance issues and missing validations.

---

## Correctness Issues

### CRITICAL: Zero-block fill-to-boundary ignores RSI boundary

**File**: `aec.go:170-175`

**Problem**: When a zero-block run hits the ROS=5 fill-to-boundary case, the Go code only fills to the next 64-block segment boundary. The CCSDS spec and libaec fill to `MIN(rsi - blocksDecoded, 64 - (blocksDecoded % 64))` — whichever of the RSI boundary and the 64-block boundary comes first.

**Reference** (libaec `decode.c`, `m_zero_block`):
```c
int b = (int)RSI_USED_SIZE(state) / strm->block_size;
zero_blocks = MIN((int)(strm->rsi - b), 64 - (b % 64));
```

**Current Go code**:
```go
segPos := blocksDecoded % 64
zeroBlocks = 64 - segPos
```

**Impact**: For any stream with `RSI < 64` that contains a zero-block run of exactly ROS length, the decoder emits too many zero samples (up to 64 blocks instead of at most RSI blocks). This misaligns the RSI boundary, corrupts subsequent reference sample positions, and applies the inverse preprocessor to wrong data segments. The output is incorrect.

**Exploitable example**: RSI=16, BlockSize=16, a zero-block run at position 14 with FS=4 (ROS=5). libaec fills 2 blocks (to RSI at position 16); this code fills 50 blocks (to segment boundary at position 64). The resulting 768 extra zero samples are wrong, and the misplaced RSI boundary throws off all subsequent decoding.

**Fix**:
```go
if zeroBlocks == ros {
    segPos := blocksDecoded % 64
    toSegment := 64 - segPos
    toRSI := p.RSI - blocksDecoded
    zeroBlocks = toSegment
    if toRSI < toSegment {
        zeroBlocks = toRSI
    }
    if zeroBlocks == 0 {
        zeroBlocks = 64
    }
}
```

### CRITICAL: RSI == 0 causes infinite loop

**File**: `aec.go:151`

**Problem**: The inner block loop `for blocksDecoded := 0; blocksDecoded < p.RSI && len(out) < p.NumValues` immediately terminates if `p.RSI == 0`, executing 0 iterations. The outer loop `for len(out) < p.NumValues` never terminates because no samples are ever produced. The result is a tight infinite loop with 100% CPU usage.

libaec explicitly rejects RSI == 0 (`return AEC_CONF_ERROR`).

**Fix**: Add parameter validation at the top of `DecodeAll`:
```go
if p.RSI == 0 {
    return nil, fmt.Errorf("aec: RSI must be > 0")
}
```

### HIGH: Bitstream underflow returns silent garbage

**File**: `bitreader.go:61-68, 70-83`

**Problem**: Both `readBits` and `readFS` return values on bitstream underflow without any error indicator:

- `readBits(n)`: If `br.bits < n` after filling, returns **0** (not an error).
- `readFS()`: If `br.bits <= 0` after filling, returns the **partial count** (not an error).

In contrast, libaec returns `AEC_STREAM_ERROR` / `AEC_DATA_ERROR` (via the FSM `M_ERROR` status) when the bitstream ends unexpectedly.

**Impact**: If a truncated or corrupted AEC stream is decoded, the output will be silently wrong — the caller gets a success result with fabricated data. In production GRIB2/HDF5 decoding, this could mean millions of incorrect weather/satellite values without any indication of failure.

**Fix**: Propagate an `io.ErrUnexpectedEOF` or equivalent error up through the decoder. Options:
1. Return `(uint64, error)` from `readBits` and `readFS`
2. Track a boolean `overflow` flag on the bitReader and check after decoding
3. In `fill` / `fillStream`, record that the stream was exhausted, and have `readBits` / `readFS` check this

### MEDIUM: No parameter validation against spec ranges

**File**: `aec.go:107-115`

libaec validates:
```c
if (strm->bits_per_sample > 32 || strm->bits_per_sample == 0
    || strm->rsi == 0
    || strm->block_size & 1 || strm->block_size == 0)
    return AEC_CONF_ERROR;
```

This library validates **none** of:
- `BitsPerSample > 32` → undefined right-shift behavior (`1 << 33` wraps mod 32 in Go runtime for non-constant shifts)
- `BlockSize == 0` → potential division issues in zero-block calculation
- `BlockSize & 1 != 0` → per spec, block sizes must be even (8, 16, 32, 64)
- `NumValues < 0` → negative `make` capacity

### MEDIUM: No 3-byte (24-bit) data support

**File**: `aec.go:63`

Flag3Byte (value 2) is defined but never handled. The spec calls for 24-bit samples packed in 3 bytes per sample. libaec handles this via the `AEC_DATA_3BYTE` flag and separate flush/put functions. This library silently ignores the flag, treating 24-bit data as regular 32-bit packed data. In GRIB2, 24-bit fields are common (e.g., ICON ensemble data) — decoding such streams would produce **wrong output**.

---

## Performance Issues

### HIGH: readFS reads 1 bit at a time

**File**: `bitreader.go:70-83`

Each call to `readFS()` does a `fill(1)` → `bits--` → bit extract → repeat loop. For a typical FS value of 3, that's 4 separate iterations, each with a function call and bounds check.

libaec's `direct_get_fs` uses CLZ (count-leading-zeros) instructions to process up to 56 bits at once:
```c
int i = 63 - __builtin_clzll(state->acc);
fs += state->bitp - i - 1;
state->bitp = i;
```

**Impact**: FS decoding is on the critical path for split-sample and SE blocks. This alone likely accounts for 3-10x slowdown vs. libaec.

**Fix**: Implement `readFS` using `math/bits.LeadingZeros64`:
```go
func (br *bitReader) readFS() int {
    br.fill(64)  // load full accumulator window
    if br.bits == 0 {
        return 0
    }
    mask := ^(uint64(0) << br.bits) // mask off unused high bits
    lz := bits.LeadingZeros64(br.acc & mask)
    consumed := lz + 1 // zeros + terminator 1
    if consumed > br.bits {
        consumed = br.bits
    }
    br.bits -= consumed
    return lz
}
```

### HIGH: fill() reads 1 byte at a time

**File**: `bitreader.go:29-38`

```go
for br.bits < n && br.pos < len(br.data) {
    br.acc = (br.acc << 8) | uint64(br.data[br.pos])
    br.pos++
    br.bits += 8
}
```

libaec's `direct_get` bulk-loads up to 7 bytes in a single operation using multi-byte OR chains. This Go code does 1 byte per iteration.

**Impact**: For every `readBits(n)`, `fill(n)` may loop n/8 times doing single-byte shifts. For a 32-bit sample read, that's 4 iterations. For a 32-bit block_size=16 no-compression block, that's 64 iterations just in `fill`.

**Fix**: Bulk-load from the byte slice:
```go
func (br *bitReader) fill(n int) {
    for br.bits < n && br.pos < len(br.data) {
        for br.bits < n && br.pos < len(br.data) {
            br.acc = (br.acc << 8) | uint64(br.data[br.pos])
            br.pos++
            br.bits += 8
        }
    }
}
```
Or use `encoding/binary` for bulk `Uint64` reading where alignment permits.

### MEDIUM: buildSETable() called per DecodeAll

**File**: `aec.go:135, 295-304`

`buildSETable()` allocates and computes the 91-entry SE table on every decode call. The table is constant — this wastes GC pressure and CPU cycles.

**Fix**: Use a package-level var with `sync.Once`:
```go
var seTable [][2]uint32
var seTableOnce sync.Once

func getSETable() [][2]uint32 {
    seTableOnce.Do(func() {
        table := make([][2]uint32, seTableSize)
        gamma := 0
        for beta := 0; gamma < seTableSize; beta++ {
            for d1 := 0; d1 <= beta && gamma < seTableSize; d1++ {
                table[gamma] = [2]uint32{uint32(beta - d1), uint32(d1)}
                gamma++
            }
        }
        seTable = table
    })
    return seTable
}
```

### MEDIUM: Zero-block output uses one-at-a-time append

**File**: `aec.go:184-186`

```go
for i := 0; i < zeroSamples && len(out) < p.NumValues; i++ {
    out = append(out, 0)
}
```

For large zero-block runs (e.g., 64 blocks * 16 samples = 1024 zeros), this does 1024 individual append calls.

**Fix**: Use slice growth + bulk fill:
```go
n := zeroSamples
if n > p.NumValues - len(out) {
    n = p.NumValues - len(out)
}
if n > 0 {
    start := len(out)
    out = out[:start+n]
    // slices in Go 1.24 default-initialize to zero
    // otherwise: for i := range out[start:] { out[start+i] = 0 }
}
```

### LOW: Fixed-size streaming buffer

**File**: `bitreader.go:17`

The streaming bitReader uses a fixed 4096-byte buffer (`rbuf [4096]byte`). For large files, a larger buffer (e.g., 64KB) would reduce syscall overhead. Consider using `io.ReadFull` with a configurable buffer.

---

## Test Coverage Gaps

The test suite covers 12 official CCSDS 121B2 vectors but misses important scenarios:

| Scenario | Missing | Why it matters |
|---|---|---|
| RSI < 64 with ROS=5 zero-block | ✓ | The RSI-boundary bug only manifests here |
| No-preprocessing (plain delta) | ✓ | All test vectors use `FlagPreprocess` |
| BlockSize != 16 (8, 32, 64) | ✓ | Different block sizes affect id_len, in_blklen |
| 3-byte (24-bit) data | ✓ | Flag3Byte is defined but untested |
| PadRSI with non-trivial alignment | ✓ | Only sar32bit tests padRSI with RSI=256 |
| Signed data (rejection) | Partial | Only error path, no valid signed vectors |
| Streaming with slow/piecemeal io.Reader | ✓ | Byte-slice decode always has full data |
| Truncated/corrupted bitstream | ✓ | Error handling never exercised |
| Edge: BlockSize=8, RSI=1 | ✓ | Minimal valid parameters |
| Edge: BitsPerSample=32, RSI=128 | ✓ | Max bit width with typical GRIB2 params |
| GRIB2 real-world | External | No in-tree integration test |

### Recommended additions
1. Libaec-generated test vectors for RSI=8 with zero-block runs at ROS=5 near the RSI boundary
2. Test vectors without preprocessing (`Flags: 0`)
3. Test vectors with BlockSize=8, 32, 64
4. Fuzz test: decode → compare against libaec output for random parameters and random data
5. Roundtrip with a libaec encoder (encode random data, decode with Go, compare)

---

## Spec Compliance Checklist

| Requirement | Status | Notes |
|---|---|---|
| §4.1 ID codes (id_len = 3/4/5) | ✓ | |
| §4.2 Split-sample (Rice) | ✓ | FS+k remainder, correct order |
| §4.3 Zero-block | ⚠️ | ROS=5 fill-to-boundary misses RSI boundary check |
| §4.4 Second extension (SE) | ✓ | SE table correct, odd/even handling correct |
| §4.5 No-compression | ✓ | |
| §4.6 RSI reference samples | ✓ | |
| §5.1 Preprocessing (unsigned) | ✓ | Matches libaec FLUSH macros |
| §5.1 Preprocessing (signed) | ✗ | Returns error (acknowledged) |
| §5.2 Restricted mode | ✗ | Returns error (acknowledged) |
| §5.3 RSI padding | ✓ | Bits alignment correct |
| §5.4 3-byte samples | ✗ | Flag defined but silently ignored |
| §5.5 MSB bit ordering | N/A | Returns []uint32, byte order not applicable |
| Data integrity on corrupt stream | ✗ | Silent data corruption on underflow |

---

## Recommendations

### Must Fix (correctness)
1. Fix zero-block ROS=5 fill-to-boundary to consider RSI boundary
2. Add RSI > 0 validation to prevent infinite loop
3. Report bitstream underflow as error instead of silent garbage

### Should Fix (robustness)
4. Validate BitsPerSample ≤ 32, BlockSize > 0, NumValues ≥ 0
5. Reject or implement Flag3Byte (24-bit packing)
6. Add structured error types

### Performance Quick Wins
7. Implement `readFS` using CLZ or bitwise scanning
8. Bulk-load bytes in `fill()`
9. Memoize SE table (package-level init)
10. Bulk-append for zero-block output

### Testing
11. Add fuzz harness comparing Go output against libaec
12. Add test vectors for RSI < 64 with zero-block runs
13. Add no-preprocessing and varied-block-size test vectors

---

## Verification Method

This review compared the Go implementation against:
1. **libaec v1.1.6 C source** (`decode.c`, `encode.c`, `libaec.h.in`) — the canonical reference implementation
2. **CCSDS 121.0-B-3 specification** — the standard defining the algorithm
3. **Existing test vectors** — 12 official CCSDS 121B2 test vectors bundled in `testdata/`

The bitreader was verified by hex-dumping test vector .rz files and tracing the bit-level decoding for small cases. The inverse preprocessor was cross-validated with the manual test case in `TestInversePreprocess` and the libaec FLUSH macro logic. The SE table was verified against libaec's `create_se_table()` by enumerating all 91 entries.
