// Copyright 2026 Shyrm Contributors
// Licensed under the Apache License, Version 2.0.

package aec

import "io"

// bitReader is a bit-level reader that works with both in-memory byte slices
// and streaming io.Reader sources.
type bitReader struct {
	// In-memory source (nil when streaming).
	data []byte
	pos  int

	// Streaming source (nil when in-memory).
	r      io.Reader
	rbuf   [4096]byte
	rstart int // next unread byte in rbuf
	rend   int // one past last valid byte in rbuf

	acc  uint64
	bits int
}

func newStreamBitReader(r io.Reader) *bitReader {
	return &bitReader{r: r}
}

func (br *bitReader) fill(n int) {
	if br.r != nil {
		br.fillStream(n)
		return
	}
	for br.bits < n && br.pos < len(br.data) {
		br.acc = (br.acc << 8) | uint64(br.data[br.pos])
		br.pos++
		br.bits += 8
	}
}

func (br *bitReader) fillStream(n int) {
	for br.bits < n {
		// Refill buffer from reader if exhausted.
		if br.rstart >= br.rend {
			nr, err := br.r.Read(br.rbuf[:])
			br.rstart = 0
			br.rend = nr
			if nr == 0 && err != nil {
				return
			}
		}
		// Feed one byte at a time into the accumulator.
		for br.bits < n && br.rstart < br.rend {
			br.acc = (br.acc << 8) | uint64(br.rbuf[br.rstart])
			br.rstart++
			br.bits += 8
		}
	}
}

func (br *bitReader) readBits(n int) uint64 {
	br.fill(n)
	if br.bits < n {
		return 0
	}
	br.bits -= n
	return (br.acc >> br.bits) & ((1 << n) - 1)
}

func (br *bitReader) readFS() int {
	count := 0
	for {
		br.fill(1)
		if br.bits <= 0 {
			return count
		}
		br.bits--
		if (br.acc>>br.bits)&1 == 1 {
			return count
		}
		count++
	}
}
