// Copyright 2026 Shyrm Contributors
// Licensed under the Apache License, Version 2.0.

package aec

import (
	"encoding/binary"
	"io"
	"math/bits"
)

// bitReader is a bit-level reader that works with both in-memory byte slices
// and streaming io.Reader sources. The accumulator holds the next "bits"
// unread bits in its low-order positions; the most significant unread bit
// sits at position (bits-1).
type bitReader struct {
	// In-memory source (nil when streaming).
	data []byte
	pos  int

	// Streaming source (nil when in-memory).
	r      io.Reader
	rbuf   [4096]byte
	rstart int
	rend   int

	acc  uint64
	bits int

	// underflow latches when a read could not be satisfied. Reads after
	// this return zero; callers check err() at end of decode.
	underflow bool
}

func newStreamBitReader(r io.Reader) *bitReader {
	return &bitReader{r: r}
}

// fill ensures the accumulator holds at least n bits, where 1 <= n <= 56.
// Sets underflow when the source is exhausted before n bits are available.
func (br *bitReader) fill(n int) {
	if br.r != nil {
		br.fillStream(n)
		return
	}
	// Bulk-fold 4 bytes at a time when there is room (bits <= 32 means at
	// least 32 free high-order bits in the 64-bit accumulator).
	for br.bits <= 32 && br.pos+4 <= len(br.data) {
		v := uint64(binary.BigEndian.Uint32(br.data[br.pos:]))
		br.acc = (br.acc << 32) | v
		br.pos += 4
		br.bits += 32
	}
	for br.bits < n && br.pos < len(br.data) {
		br.acc = (br.acc << 8) | uint64(br.data[br.pos])
		br.pos++
		br.bits += 8
	}
	if br.bits < n {
		br.underflow = true
	}
}

func (br *bitReader) fillStream(n int) {
	for br.bits < n {
		if br.rstart >= br.rend {
			nr, err := br.r.Read(br.rbuf[:])
			br.rstart = 0
			br.rend = nr
			if nr == 0 {
				if err != nil {
					br.underflow = true
					return
				}
				continue
			}
		}
		for br.bits <= 32 && br.rend-br.rstart >= 4 {
			v := uint64(binary.BigEndian.Uint32(br.rbuf[br.rstart:]))
			br.acc = (br.acc << 32) | v
			br.rstart += 4
			br.bits += 32
		}
		for br.bits < n && br.rstart < br.rend {
			br.acc = (br.acc << 8) | uint64(br.rbuf[br.rstart])
			br.rstart++
			br.bits += 8
		}
	}
}

// readBits reads n bits (1 <= n <= 56). Returns 0 on underflow.
func (br *bitReader) readBits(n int) uint64 {
	if br.bits < n {
		br.fill(n)
		if br.bits < n {
			return 0
		}
	}
	br.bits -= n
	return (br.acc >> br.bits) & ((1 << n) - 1)
}

// readFS reads a fundamental-sequence value: number of leading zero bits,
// consuming the terminating 1 bit. Uses LeadingZeros64 to scan up to 56 bits
// per fill instead of bit-at-a-time.
func (br *bitReader) readFS() int {
	count := 0
	for {
		if br.bits == 0 {
			br.fill(1)
			if br.bits == 0 {
				return count
			}
		}
		// The live bits occupy positions 0..bits-1. Shift them so the top
		// live bit lands at position 63, then count leading zeros.
		live := br.acc << (64 - br.bits)
		lz := bits.LeadingZeros64(live)
		if lz >= br.bits {
			// All live bits are zero. Consume them and refill.
			count += br.bits
			br.bits = 0
			continue
		}
		count += lz
		br.bits -= lz + 1 // zeros + terminator
		return count
	}
}

// err returns io.ErrUnexpectedEOF if any read overran the source.
func (br *bitReader) err() error {
	if br.underflow {
		return io.ErrUnexpectedEOF
	}
	return nil
}
