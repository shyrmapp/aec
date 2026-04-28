// Copyright 2026 Shyrm Contributors
// Licensed under the Apache License, Version 2.0.
// See LICENSE file for details.

// Package aec implements a pure-Go decoder for CCSDS 121.0-B-3
// (Adaptive Entropy Coding), the lossless compression standard used by
// GRIB2 Data Representation Template 5.42, the HDF5 szip filter, and
// satellite telemetry systems.
//
// This is the only known pure-Go implementation of CCSDS 121. It requires
// no cgo, no external C libraries, and cross-compiles to any platform
// Go supports.
//
// # Supported features
//
//   - All coding options: split-sample (Rice/Golomb), zero-block, second extension, no-compression
//   - NN (nearest-neighbour) preprocessing / postprocessing
//   - Unsigned data, 1–32 bits per sample
//   - Non-restricted mode
//
// # Usage
//
// Decode from a byte slice:
//
//	decoded, err := aec.Decode(compressed, aec.Params{
//	    BitsPerSample: 16,
//	    BlockSize:     16,
//	    RSI:           128,
//	    Flags:         aec.FlagMSB,
//	    NumValues:     1024,
//	})
//
// Stream from an [io.Reader]:
//
//	dec := aec.NewDecoder(r, aec.Params{
//	    BitsPerSample: 16,
//	    BlockSize:     16,
//	    RSI:           128,
//	    Flags:         aec.FlagMSB,
//	    NumValues:     1024,
//	})
//	decoded, err := dec.DecodeAll()
//
// # Unsupported features
//
// Signed data ([FlagSigned]) and restricted coding ([FlagRestricted])
// are not implemented and return an error. [Flag3Byte] is accepted but has
// no effect: the decoder's output is always []uint32, so 3-byte output
// packing is the caller's concern.
//
// # References
//
//   - CCSDS 121.0-B-3: https://public.ccsds.org/Pubs/121x0b3.pdf
//   - libaec (C reference): https://github.com/MathisRosenhauer/libaec
//   - GRIB2 template 5.42: WMO Manual on Codes, Part B
package aec

import (
	"fmt"
	"io"
)

// Coding option flags, matching the libaec/CCSDS 121.0-B constants.
const (
	FlagSigned     = 1  // AEC_DATA_SIGNED (not supported)
	Flag3Byte      = 2  // AEC_DATA_3BYTE (no effect: output is []uint32)
	FlagMSB        = 4  // AEC_DATA_MSB — most-significant bit first
	FlagPreprocess = 8  // AEC_DATA_PREPROCESS — NN preprocessing enabled
	FlagRestricted = 16 // AEC_RESTRICTED (not supported)
	FlagPadRSI     = 32 // AEC_PAD_RSI — RSI byte-boundary padding
)

// Params configures the AEC decoder.
type Params struct {
	BitsPerSample int // Sample depth in bits (1–32).
	BlockSize     int // Number of samples per block (8, 16, 32, or 64).
	RSI           int // Reference Sample Interval in blocks (>0).
	Flags         int // Bitwise OR of Flag* constants.
	NumValues     int // Total number of samples to decode.
}

// Decode decompresses a CCSDS 121.0-B encoded byte stream from a byte slice.
// This is a convenience wrapper around [NewDecoder] for callers that already
// have the full compressed payload in memory.
func Decode(compressed []byte, p Params) ([]uint32, error) {
	return newDecoderFromBits(&bitReader{data: compressed}, p).DecodeAll()
}

// Decoder reads AEC-compressed data from an [io.Reader].
type Decoder struct {
	br     *bitReader
	params Params
}

// NewDecoder creates a streaming AEC decoder that reads compressed data
// from r. Call [Decoder.DecodeAll] to decompress all values.
func NewDecoder(r io.Reader, p Params) *Decoder {
	return &Decoder{
		br:     newStreamBitReader(r),
		params: p,
	}
}

func newDecoderFromBits(br *bitReader, p Params) *Decoder {
	return &Decoder{br: br, params: p}
}

// DecodeAll decompresses all values and returns them as a uint32 slice.
func (d *Decoder) DecodeAll() ([]uint32, error) {
	p := d.params

	if p.NumValues < 0 {
		return nil, fmt.Errorf("aec: NumValues must be >= 0")
	}
	if p.NumValues == 0 {
		return nil, nil
	}
	if p.BitsPerSample < 0 || p.BitsPerSample > 32 {
		return nil, fmt.Errorf("aec: BitsPerSample must be 0..32, got %d", p.BitsPerSample)
	}
	if p.BitsPerSample == 0 {
		return make([]uint32, p.NumValues), nil
	}
	if p.Flags&FlagSigned != 0 {
		return nil, fmt.Errorf("aec: signed data not supported")
	}
	if p.Flags&FlagRestricted != 0 {
		return nil, fmt.Errorf("aec: restricted mode not supported")
	}
	if p.RSI <= 0 {
		return nil, fmt.Errorf("aec: RSI must be > 0, got %d", p.RSI)
	}
	if p.BlockSize <= 0 || p.BlockSize&1 != 0 {
		return nil, fmt.Errorf("aec: BlockSize must be even and > 0, got %d", p.BlockSize)
	}

	preprocess := p.Flags&FlagPreprocess != 0
	padRSI := p.Flags&FlagPadRSI != 0
	var idLen int
	switch {
	case p.BitsPerSample > 16:
		idLen = 5
	case p.BitsPerSample > 8:
		idLen = 4
	default:
		idLen = 3
	}

	br := d.br
	out := make([]uint32, 0, p.NumValues)
	xmax := uint32(0xFFFFFFFF)
	if p.BitsPerSample < 32 {
		xmax = uint32((1 << p.BitsPerSample) - 1)
	}

	firstRSI := true
	for len(out) < p.NumValues {
		if padRSI && !firstRSI {
			// Discard remaining bits in the current byte to align
			// to a byte boundary (CCSDS 121.0-B-3 §4.3.2).
			br.bits -= br.bits % 8
		}
		firstRSI = false
		rsiStart := len(out)
		refPending := true

		for blocksDecoded := 0; blocksDecoded < p.RSI && len(out) < p.NumValues; {
			id := br.readBits(idLen)
			codedInBlock := p.BlockSize

			switch {
			case id == 0:
				// Low-entropy: sub-bit is read BEFORE reference (libaec order:
				// m_id -> m_low_entropy reads sub-bit -> m_low_entropy_ref reads ref).
				// Note: codedInBlock is unused in this branch — zero-block and SE
				// each track their own sample count via blocksDecoded / `i`.
				subBit := br.readBits(1)
				if refPending {
					out = append(out, uint32(br.readBits(p.BitsPerSample)))
					refPending = false
				}
				if subBit == 0 {
					// Zero-block.
					const ros = 5
					fs := br.readFS()
					zeroBlocks := fs + 1
					if zeroBlocks == ros {
						// Fill-to-boundary: clamp by both segment (64-block)
						// and RSI boundaries (libaec m_zero_block).
						segPos := blocksDecoded % 64
						toSeg := 64 - segPos
						toRSI := p.RSI - blocksDecoded
						zeroBlocks = toSeg
						if toRSI < zeroBlocks {
							zeroBlocks = toRSI
						}
						if zeroBlocks <= 0 {
							zeroBlocks = 64
						}
					} else if zeroBlocks > ros {
						zeroBlocks--
					}
					refSamples := 0
					if blocksDecoded == 0 {
						refSamples = 1
					}
					zeroSamples := zeroBlocks*p.BlockSize - refSamples
					remaining := p.NumValues - len(out)
					if zeroSamples > remaining {
						zeroSamples = remaining
					}
					if zeroSamples > 0 {
						out = append(out, make([]uint32, zeroSamples)...)
					}
					blocksDecoded += zeroBlocks
					continue
				}
				// Second extension: libaec m_se starts at i=state->ref.
				i := 0
				if blocksDecoded == 0 {
					i = 1
				}
				for i < p.BlockSize && len(out) < p.NumValues {
					gamma := br.readFS()
					if gamma >= seTableSize {
						return nil, fmt.Errorf("aec: SE gamma %d out of range", gamma)
					}
					d0, d1 := seTable[gamma][0], seTable[gamma][1]
					if i&1 == 0 {
						out = append(out, d0)
						i++
					}
					out = append(out, d1)
					i++
				}

			case id == (1<<idLen)-1:
				// No compression.
				if refPending {
					out = append(out, uint32(br.readBits(p.BitsPerSample)))
					codedInBlock = p.BlockSize - 1
					refPending = false
				}
				for i := 0; i < codedInBlock && len(out) < p.NumValues; i++ {
					out = append(out, uint32(br.readBits(p.BitsPerSample)))
				}

			default:
				// Split-sample (Rice/Golomb), k = id - 1.
				if refPending {
					out = append(out, uint32(br.readBits(p.BitsPerSample)))
					codedInBlock = p.BlockSize - 1
					refPending = false
				}
				// All FS values first, then all k-bit remainders (libaec m_split).
				k := int(id) - 1
				startIdx := len(out)
				for i := 0; i < codedInBlock; i++ {
					out = append(out, uint32(br.readFS())<<k)
				}
				if k > 0 {
					for i := 0; i < codedInBlock; i++ {
						out[startIdx+i] += uint32(br.readBits(k))
					}
				}
			}
			blocksDecoded++
		}

		// Postprocessor: inverse mapper + inverse predictor.
		if preprocess {
			rsiEnd := len(out)
			if rsiEnd > p.NumValues {
				rsiEnd = p.NumValues
			}
			if rsiEnd > rsiStart+1 {
				inversePreprocess(out[rsiStart:rsiEnd], xmax)
			}
		}
	}

	if len(out) > p.NumValues {
		out = out[:p.NumValues]
	}
	if err := br.err(); err != nil {
		return out, err
	}
	return out, nil
}

// inversePreprocess reverses NN preprocessing: inverse mapper then inverse predictor.
func inversePreprocess(samples []uint32, xmax uint32) {
	prev := samples[0]
	for i := 1; i < len(samples); i++ {
		d := samples[i]
		var theta uint32
		if prev <= xmax-prev {
			theta = prev
		} else {
			theta = xmax - prev
		}
		var val uint32
		if d <= 2*theta {
			if d&1 == 0 {
				val = (prev + d/2) & xmax
			} else {
				val = (prev - (d+1)/2) & xmax
			}
		} else {
			if theta == prev {
				val = d & xmax
			} else {
				val = (xmax - d) & xmax
			}
		}
		samples[i] = val
		prev = val
	}
}

// seTableSize matches libaec's SE_TABLE_SIZE (91 entries, gamma 0–90).
// The SE coding option is only selected by the encoder for low-entropy blocks,
// so gamma values stay small regardless of bitsPerSample.
const seTableSize = 91

// seTable is the precomputed second-extension lookup table.
var seTable = func() [][2]uint32 {
	table := make([][2]uint32, seTableSize)
	gamma := 0
	for beta := 0; gamma < seTableSize; beta++ {
		for d1 := 0; d1 <= beta && gamma < seTableSize; d1++ {
			table[gamma] = [2]uint32{uint32(beta - d1), uint32(d1)}
			gamma++
		}
	}
	return table
}()

// buildSETable returns a fresh copy of the SE lookup table. Retained for
// tests that want to verify table generation independently.
func buildSETable() [][2]uint32 {
	out := make([][2]uint32, seTableSize)
	copy(out, seTable)
	return out
}
