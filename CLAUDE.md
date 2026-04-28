# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Pure-Go decoder for CCSDS 121.0-B-3 (Adaptive Entropy Coding). Decode-only. No cgo. Used by GRIB2 template 5.42, HDF5 szip, satellite telemetry. Module: `github.com/shyrmapp/aec`. Go 1.24+.

Single-package library — `package aec`. Three source files: `aec.go` (decoder + Params + flags), `bitreader.go` (bit-level reader, both `[]byte` and `io.Reader`), `aec_test.go` (unit + CCSDS vector tests + benchmarks).

## Commands

```bash
go test ./...                                   # all tests
go test -run TestCCSDS_AllOptions ./...         # one test group
go test -run TestCCSDS_AllOptions/p256n08 ./... # one CCSDS vector subtest
go test -bench=. -benchmem ./...                # benchmarks
go vet ./...
go build ./...
```

Test vectors live in `testdata/` (CCSDS 121B2 official set from MathisRosenhauer/libaec, BSD-2). Covers AllOptions n01–n32 (block 16, RSI 16/32), LowEntropyOptions Lowset1/2/3 n01–n08 (RSI 64), ExtendedParameters j16/r256 and j64/r4096. Tests `t.Skipf` when files missing — never fail hard on missing testdata. `numValues` and `referenceToUint32` use libaec's default packing: 17–32-bit samples occupy 4 bytes per sample (not 3) unless `-3` was used at encode time.

`testdata/ecmwf_aifs_t.aec` is a Section-7 AEC payload extracted from a real ECMWF AIFS open-data GRIB2 message (template 5.42). `TestRealWorldECMWFAIFS` decodes it and compares the SHA256 of the LE-uint32 output against a reference produced by libaec 1.1.6's `aec_buffer_decode`. This is the ground-truth regression test for production traffic — if its hash ever changes, the decoder is wrong on real data, regardless of what the synthetic CCSDS vectors say.

## Architecture

**Entry points** (`aec.go`):
- `Decode([]byte, Params) ([]uint32, error)` — wraps in-memory `bitReader`.
- `NewDecoder(io.Reader, Params).DecodeAll()` — streaming variant. Both share `Decoder.DecodeAll`.

**`Params`** maps 1:1 to GRIB2 section 5 / szip metadata. `Flags` is bitwise OR of `Flag*` constants matching libaec's `AEC_DATA_*`. Supported: `FlagMSB`, `FlagPreprocess`, `FlagPadRSI`. `FlagSigned` and `FlagRestricted` return error by design. `Flag3Byte` is accepted but has no effect (output is always `[]uint32`).

**Validation** (`DecodeAll`) rejects: `NumValues<0`, `BitsPerSample` outside `0..32`, `RSI<=0`, `BlockSize<=0` or odd. `RSI=0` previously caused an infinite loop — keep these checks.

**Decode loop** (`DecodeAll`):
1. Pick `idLen` from `BitsPerSample` (3/4/5 bits — matches libaec).
2. Outer loop: RSIs. If `FlagPadRSI` and not first RSI, drop bits to byte boundary (CCSDS §4.3.2).
3. Inner loop: blocks until RSI full or `NumValues` reached. Read `id`:
   - `id == 0` → low-entropy. Sub-bit read **before** ref sample (libaec order: m_id → m_low_entropy → m_low_entropy_ref). Sub-bit 0 = zero-block (FS run with ROS=5 escape and segment-aligned 64-cap), sub-bit 1 = second extension via `seTable`.
   - `id == (1<<idLen)-1` → no-compression, raw `BitsPerSample` per sample.
   - else → split-sample, k = id-1. **All FS values first, then all k-bit remainders** (libaec m_split layout, not interleaved).
4. First block of each RSI carries reference sample (`refPending`) consuming one slot.
5. After RSI: if `FlagPreprocess`, run `inversePreprocess` (inverse mapper + inverse predictor, NN postprocessing).

**`bitReader`** (`bitreader.go`): MSB-first 64-bit accumulator. `data` set → in-memory; `r` set → 4096-byte buffered stream. `fill` batches 4 bytes at a time when `bits<=32`. `readFS` uses `math/bits.LeadingZeros64` over the live window. Underflow is latched in `bitReader.underflow`; `bitReader.err()` returns `io.ErrUnexpectedEOF` and `DecodeAll` propagates it. Reads after underflow return zero — callers must check `err()` (already done in `DecodeAll`).

**`seTable`** (size 91, gamma 0–90): CCSDS 121 SE_TABLE. Package-level `var` initialized once at startup. Encoder only picks SE for low-entropy blocks so gamma stays small regardless of bit depth.

## Invariants when modifying

- Match libaec bit-stream order exactly. Order of low-entropy reads (sub-bit before ref) and split-sample layout (all FS, then all k-bit) are not arbitrary — diverging breaks every test vector.
- Zero-block ROS=5 escape: `fs+1 == 5` triggers fill-to-boundary clamped by **both** `64 - blocksDecoded%64` and `RSI - blocksDecoded` (libaec `m_zero_block`). `fs+1 > 5` decrements by one. Don't simplify — clamping by only the segment boundary is the prior bug exercised by `TestZeroBlockROSBoundary`.
- SE branch: `i = 1` when `blocksDecoded == 0` (skips ref slot already filled). Two samples per gamma `[d0, d1]`; `d0` written only on even `i`.
- `inversePreprocess` operates per-RSI slice, never across RSIs.
- All sample buffers are `uint32`; signed support deliberately absent.
- Error path: any short read latches `bitReader.underflow`. `DecodeAll` returns `io.ErrUnexpectedEOF` from `br.err()` after the loop. Don't bail early on the flag — finishing the loop keeps state consistent for inspection.

## Reference

CCSDS 121.0-B-3 spec: https://public.ccsds.org/Pubs/121x0b3.pdf
libaec C reference: https://github.com/MathisRosenhauer/libaec — when in doubt about bit-stream behaviour, cross-check libaec's `decode.c` (`m_id`, `m_split`, `m_low_entropy`, `m_se`, `m_zero_block`, `m_uncomp`).
