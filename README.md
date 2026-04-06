# aec

Pure-Go decoder for **CCSDS 121.0-B-3** (Adaptive Entropy Coding) — the lossless compression used by GRIB2 template 5.42, HDF5 szip, and satellite telemetry.

No cgo. No external C libraries. Cross-compiles to any platform Go supports.

## Install

```
go get github.com/shyrmapp/aec
```

Requires Go 1.24+.

## Usage

### Decode from a byte slice

```go
decoded, err := aec.Decode(compressed, aec.Params{
    BitsPerSample: 16,
    BlockSize:     16,
    RSI:           128,
    Flags:         aec.FlagMSB,
    NumValues:     542040,
})
```

### Stream from an io.Reader

For large files where you don't want the entire compressed payload in memory:

```go
f, _ := os.Open("data.aec")
defer f.Close()

dec := aec.NewDecoder(f, aec.Params{
    BitsPerSample: 16,
    BlockSize:     16,
    RSI:           128,
    Flags:         aec.FlagMSB,
    NumValues:     542040,
})
decoded, err := dec.DecodeAll()
```

### GRIB2 template 5.42

AEC is the compression used by WMO GRIB2 Data Representation Template 5.42. The `Params` map directly from the GRIB2 section 5 fields:

```go
params := aec.Params{
    BitsPerSample: bitsPerValue,          // octet 12
    BlockSize:     blockSize,             // octet 18 (typically 16)
    RSI:           referenceInterval,     // octet 20 (typically 128)
    Flags:         codingFlags,           // octet 22 (bitwise OR of AEC flags)
    NumValues:     numberOfDataPoints,    // section 5, octet 6-9
}
```

### HDF5 szip filter

AEC is the underlying algorithm for the szip compression filter in HDF5. The same `Params` struct applies — consult the szip filter metadata for the encoding parameters.

## Supported features

- All coding options: split-sample (Rice/Golomb), zero-block, second extension, no-compression
- NN (nearest-neighbour) preprocessing / postprocessing
- RSI-aligned padding (`FlagPadRSI`)
- Unsigned data, 1–32 bits per sample
- In-memory and streaming decode
- Validated against all official CCSDS 121B2 test vectors (1–32 bit, all coding modes, low entropy, extended parameters)
- Production-tested on DWD ICON-D2/ICON-EU GRIB2 NWP data

## Limitations

- **No encoder** — decode only (contributions welcome)
- **No signed data** — `FlagSigned` returns an error
- **No restricted mode** — `FlagRestricted` returns an error

## Why this exists

Every Go project that needs to read AEC-compressed data (GRIB2 weather models, satellite imagery, HDF5 szip) currently requires cgo bindings to [libaec](https://github.com/MathisRosenhauer/libaec). This makes cross-compilation painful and deployment to containers harder than it should be.

This package was extracted from [Shyrm](https://shyrm.app), a precipitation nowcasting app that decodes DWD ICON-D2 and ICON-EU GRIB2 files in real time.

## References

- [CCSDS 121.0-B-3](https://public.ccsds.org/Pubs/121x0b3.pdf) — the standard
- [libaec](https://github.com/MathisRosenhauer/libaec) — C reference implementation
- [WMO GRIB2 Manual](https://library.wmo.int/idurl/4/35625) — template 5.42 specification

## License

Apache 2.0 — see [LICENSE](LICENSE).

Test vectors in `testdata/` are from the [CCSDS 121B2 reference data set](https://github.com/MathisRosenhauer/libaec), distributed under the BSD 2-Clause license.
