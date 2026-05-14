// Copyright 2026 Shyrm Contributors
// Licensed under the Apache License, Version 2.0.

package aec

import (
	"encoding/binary"
	"io"
	"math/bits"
)

// streamBufSize is the streaming refill buffer. Sized to match typical OS
// readahead so each Read() amortizes well; allocated lazily only when the
// streaming constructor is used so the in-memory path stays cheap.
const streamBufSize = 64 * 1024

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
	rbuf   []byte // sized to streamBufSize when streaming
	rstart int
	rend   int

	acc  uint64
	bits int

	// underflow latches when a read could not be satisfied. Reads after
	// this return zero; callers check err() at end of decode.
	underflow bool
}

func newStreamBitReader(r io.Reader) *bitReader {
	return &bitReader{r: r, rbuf: make([]byte, streamBufSize)}
}

// fill ensures the accumulator holds at least n bits, where 1 <= n <= 56.
// Sets underflow when the source is exhausted before n bits are available.
func (br *bitReader) fill(n int) {
	br.fillUpTo(n)
	if br.bits < n {
		br.underflow = true
	}
}

// fillUpTo loads up to n bits without latching underflow when the source is
// short. Used by readFS to pre-fill a 56-bit window even when fewer bits
// remain — the FS scan handles trailing-bit underflow via its empty-window
// branch, so latching here would surface a false error.
func (br *bitReader) fillUpTo(n int) {
	if br.r != nil {
		br.fillStreamUpTo(n)
		return
	}
	// 8-byte fast path: when the accumulator is empty there is room for a
	// full 64-bit load. Cheaper than two 32-bit folds.
	if br.bits == 0 && br.pos+8 <= len(br.data) {
		br.acc = binary.BigEndian.Uint64(br.data[br.pos:])
		br.pos += 8
		br.bits = 64
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
}

func (br *bitReader) fillStream(n int) {
	br.fillStreamUpTo(n)
	if br.bits < n {
		br.underflow = true
	}
}

func (br *bitReader) fillStreamUpTo(n int) {
	for br.bits < n {
		if br.rstart >= br.rend {
			nr, err := br.r.Read(br.rbuf)
			br.rstart = 0
			br.rend = nr
			if nr == 0 {
				if err != nil {
					// Source exhausted; callers wrapping via fill(n)
					// observe bits < n and latch underflow themselves.
					return
				}
				continue
			}
		}
		// Mirror the in-memory 8-byte fast path.
		if br.bits == 0 && br.rend-br.rstart >= 8 {
			br.acc = binary.BigEndian.Uint64(br.rbuf[br.rstart:])
			br.rstart += 8
			br.bits = 64
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
// n > 56 is a programmer error — the (1<<n)-1 mask breaks past 63 — so
// the function returns 0 without consuming bits.
func (br *bitReader) readBits(n int) uint64 {
	if n <= 0 || n > 56 {
		return 0
	}
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
// consuming the terminating 1 bit. Pre-fills 56 bits so common FS values
// (0-10) finish in a single LeadingZeros64 scan.
func (br *bitReader) readFS() int {
	count := 0
	for {
		if br.bits == 0 {
			br.fillUpTo(56)
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
