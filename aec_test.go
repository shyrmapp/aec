// Copyright 2026 Shyrm Contributors
// Licensed under the Apache License, Version 2.0.

package aec

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"
)

// --- Unit tests ---

func TestInversePreprocess(t *testing.T) {
	samples := []uint32{100, 8, 5, 12, 17, 24, 29, 26}
	inversePreprocess(samples, 255)
	want := []uint32{100, 104, 101, 107, 98, 110, 95, 108}
	for i := range samples {
		if samples[i] != want[i] {
			t.Errorf("samples[%d] = %d, want %d", i, samples[i], want[i])
		}
	}
}

func TestDecodeEmpty(t *testing.T) {
	out, err := Decode(nil, Params{NumValues: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("len = %d, want 0", len(out))
	}
}

func TestDecodeZeroBits(t *testing.T) {
	out, err := Decode(nil, Params{NumValues: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 10 {
		t.Fatalf("len = %d, want 10", len(out))
	}
	for i, v := range out {
		if v != 0 {
			t.Errorf("out[%d] = %d, want 0", i, v)
		}
	}
}

func TestSETable(t *testing.T) {
	want := [][2]uint32{{0, 0}, {1, 0}, {0, 1}, {2, 0}}
	for i, w := range want {
		if seTable[i] != w {
			t.Errorf("seTable[%d] = %v, want %v", i, seTable[i], w)
		}
	}
}

func TestBitReader(t *testing.T) {
	br := &bitReader{data: []byte{0xEF, 0xE7}}
	if v := br.readBits(4); v != 14 {
		t.Errorf("readBits(4) = %d, want 14", v)
	}
	if v := br.readBits(8); v != 254 {
		t.Errorf("readBits(8) = %d, want 254", v)
	}
	if v := br.readBits(4); v != 7 {
		t.Errorf("readBits(4) = %d, want 7", v)
	}
}

func TestBitReaderFS(t *testing.T) {
	br := &bitReader{data: []byte{0b00010000}}
	if v := br.readFS(); v != 3 {
		t.Errorf("readFS = %d, want 3", v)
	}
}

func TestUnsupportedFlags(t *testing.T) {
	_, err := Decode(nil, Params{NumValues: 1, BitsPerSample: 8, Flags: FlagSigned})
	if err == nil {
		t.Error("expected error for signed data")
	}
	_, err = Decode(nil, Params{NumValues: 1, BitsPerSample: 8, Flags: FlagRestricted})
	if err == nil {
		t.Error("expected error for restricted mode")
	}
}

// TestParamValidation locks in rejection of out-of-spec parameters.
// Without these checks RSI=0 caused an infinite loop and BlockSize=0 produced
// undefined output.
func TestParamValidation(t *testing.T) {
	cases := []struct {
		name string
		p    Params
	}{
		{"RSI zero", Params{NumValues: 16, BitsPerSample: 8, BlockSize: 16, RSI: 0}},
		{"RSI negative", Params{NumValues: 16, BitsPerSample: 8, BlockSize: 16, RSI: -1}},
		{"BlockSize zero", Params{NumValues: 16, BitsPerSample: 8, BlockSize: 0, RSI: 16}},
		{"BlockSize odd", Params{NumValues: 16, BitsPerSample: 8, BlockSize: 15, RSI: 16}},
		{"BitsPerSample > 32", Params{NumValues: 16, BitsPerSample: 33, BlockSize: 16, RSI: 16}},
		{"BitsPerSample negative", Params{NumValues: 16, BitsPerSample: -1, BlockSize: 16, RSI: 16}},
		{"NumValues negative", Params{NumValues: -1, BitsPerSample: 8, BlockSize: 16, RSI: 16}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Decode(nil, c.p); err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

// TestTruncatedStream verifies that decoding a truncated payload reports
// io.ErrUnexpectedEOF instead of returning silently-wrong data.
func TestTruncatedStream(t *testing.T) {
	full := loadTestFile(t, "testdata/test_p256n08.rz")
	if len(full) < 4 {
		t.Skip("test vector too small")
	}
	truncated := full[:len(full)/2]
	p := Params{
		BitsPerSample: 8, BlockSize: 16, RSI: 16,
		Flags: FlagPreprocess, NumValues: 256,
	}
	_, err := Decode(truncated, p)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("got err=%v, want io.ErrUnexpectedEOF", err)
	}
}

// TestStreamingTruncated reaches the streaming underflow path.
func TestStreamingTruncated(t *testing.T) {
	full := loadTestFile(t, "testdata/test_p256n08.rz")
	if len(full) < 4 {
		t.Skip("test vector too small")
	}
	dec := NewDecoder(bytes.NewReader(full[:len(full)/2]), Params{
		BitsPerSample: 8, BlockSize: 16, RSI: 16,
		Flags: FlagPreprocess, NumValues: 256,
	})
	_, err := dec.DecodeAll()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("got err=%v, want io.ErrUnexpectedEOF", err)
	}
}

// slowReader returns 1 byte per Read to exercise piecemeal io.Reader handling.
type slowReader struct{ data []byte }

func (s *slowReader) Read(p []byte) (int, error) {
	if len(s.data) == 0 {
		return 0, io.EOF
	}
	if len(p) > 0 {
		p[0] = s.data[0]
		s.data = s.data[1:]
		return 1, nil
	}
	return 0, nil
}

// TestStreamingSlowReader verifies the streaming path is correct when the
// reader hands out one byte at a time.
func TestStreamingSlowReader(t *testing.T) {
	compressed := loadTestFile(t, "testdata/test_p256n08.rz")
	p := Params{
		BitsPerSample: 8, BlockSize: 16, RSI: 16,
		Flags: FlagPreprocess, NumValues: 256,
	}
	want, err := Decode(compressed, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewDecoder(&slowReader{data: append([]byte(nil), compressed...)}, p).DecodeAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("len mismatch got=%d want=%d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("mismatch at %d: got=%d want=%d", i, got[i], want[i])
		}
	}
}

// bitWriter packs bits MSB-first to match the AEC bitstream layout.
type bitWriter struct {
	buf  []byte
	cur  byte
	used int
}

func (w *bitWriter) write(value uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		bit := byte((value >> i) & 1)
		w.cur = (w.cur << 1) | bit
		w.used++
		if w.used == 8 {
			w.buf = append(w.buf, w.cur)
			w.cur, w.used = 0, 0
		}
	}
}

func (w *bitWriter) bytes() []byte {
	if w.used == 0 {
		return w.buf
	}
	return append(w.buf, w.cur<<(8-w.used))
}

// TestZeroBlockROSBoundary exposes the prior fill-to-boundary bug. The stream
// contains two RSIs (each 2 blocks of 8 samples). RSI 1 starts with a
// zero-block low-entropy block carrying FS=4, which encodes ROS=5 (the
// fill-to-boundary escape).
//
// Correct behaviour (fixed code): clamp zero-fill to MIN(64-blocksDecoded%64,
// RSI-blocksDecoded) = MIN(64, 2) = 2 blocks. RSI 1 ends. Decoder then reads
// RSI 2's two no-compression blocks and emits the literal samples.
//
// Buggy behaviour (old code): clamp to 64-segPos = 64. Inner loop fills 16
// zeros (capped by NumValues). RSI 2 is never decoded — output ends up all
// zeros, mismatching the literal RSI 2 values.
func TestZeroBlockROSBoundary(t *testing.T) {
	w := &bitWriter{}
	// RSI 1, block 0: low-entropy zero-block.
	w.write(0b000, 3)   // id=0
	w.write(0, 1)       // sub-bit = 0 (zero-block)
	w.write(0x00, 8)    // reference sample = 0
	w.write(0b00001, 5) // FS = 4 → zeroBlocks = 5 → ROS escape
	// RSI 2, block 0: no compression. id=7, ref=0xAA, then 7 × 0xBB.
	w.write(0b111, 3)
	w.write(0xAA, 8)
	for i := 0; i < 7; i++ {
		w.write(0xBB, 8)
	}
	// RSI 2, block 1: no compression. id=7, 8 × 0xCC.
	w.write(0b111, 3)
	for i := 0; i < 8; i++ {
		w.write(0xCC, 8)
	}

	out, err := Decode(w.bytes(), Params{
		BitsPerSample: 8, BlockSize: 8, RSI: 2,
		NumValues: 32, // 2 RSIs × 2 blocks × 8 samples
	})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := make([]uint32, 32)
	// RSI 1: 16 zeros (already zero-init).
	// RSI 2 block 0: 0xAA, 0xBB×7
	want[16] = 0xAA
	for i := 17; i < 24; i++ {
		want[i] = 0xBB
	}
	// RSI 2 block 1: 0xCC×8
	for i := 24; i < 32; i++ {
		want[i] = 0xCC
	}
	for i := range out {
		if out[i] != want[i] {
			t.Errorf("out[%d]=%#x, want %#x", i, out[i], want[i])
		}
	}
}

// --- Streaming API tests ---

func TestStreamingDecoder(t *testing.T) {
	// Decode the same data via []byte and io.Reader, verify identical output.
	compressed := loadTestFile(t, "testdata/test_p256n04-basic.rz")
	p := Params{
		BitsPerSample: 4,
		BlockSize:     16,
		RSI:           16,
		Flags:         FlagPreprocess,
		NumValues:     numValues("testdata/test_p256n04.dat", 4),
	}

	want, err := Decode(compressed, p)
	if err != nil {
		t.Fatal("byte decode:", err)
	}

	dec := NewDecoder(bytes.NewReader(compressed), p)
	got, err := dec.DecodeAll()
	if err != nil {
		t.Fatal("stream decode:", err)
	}

	if len(got) != len(want) {
		t.Fatalf("stream len=%d, byte len=%d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("mismatch at [%d]: stream=%d, byte=%d", i, got[i], want[i])
			break
		}
	}
}

// --- CCSDS 121B2 test vector tests ---

// testVector defines a CCSDS 121B2 test case.
type testVector struct {
	name          string
	compressed    string // path to .rz file
	reference     string // path to .dat file
	bitsPerSample int
	blockSize     int
	rsi           int
	flags         int
}

var allOptionsVectors = []testVector{
	// 1–8 bit: BlockSize=16, RSI=16
	{"p256n01-basic", "testdata/test_p256n01-basic.rz", "testdata/test_p256n01.dat", 1, 16, 16, FlagPreprocess},
	{"p256n02-basic", "testdata/test_p256n02-basic.rz", "testdata/test_p256n02.dat", 2, 16, 16, FlagPreprocess},
	{"p256n03-basic", "testdata/test_p256n03-basic.rz", "testdata/test_p256n03.dat", 3, 16, 16, FlagPreprocess},
	{"p256n04-basic", "testdata/test_p256n04-basic.rz", "testdata/test_p256n04.dat", 4, 16, 16, FlagPreprocess},
	{"p256n05", "testdata/test_p256n05.rz", "testdata/test_p256n05.dat", 5, 16, 16, FlagPreprocess},
	{"p256n06", "testdata/test_p256n06.rz", "testdata/test_p256n06.dat", 6, 16, 16, FlagPreprocess},
	{"p256n07", "testdata/test_p256n07.rz", "testdata/test_p256n07.dat", 7, 16, 16, FlagPreprocess},
	{"p256n08", "testdata/test_p256n08.rz", "testdata/test_p256n08.dat", 8, 16, 16, FlagPreprocess},
	// 9–16 bit: BlockSize=16, RSI=16 (idLen=4)
	{"p256n09", "testdata/test_p256n09.rz", "testdata/test_p256n09.dat", 9, 16, 16, FlagPreprocess},
	{"p256n10", "testdata/test_p256n10.rz", "testdata/test_p256n10.dat", 10, 16, 16, FlagPreprocess},
	{"p256n11", "testdata/test_p256n11.rz", "testdata/test_p256n11.dat", 11, 16, 16, FlagPreprocess},
	{"p256n12", "testdata/test_p256n12.rz", "testdata/test_p256n12.dat", 12, 16, 16, FlagPreprocess},
	{"p256n13", "testdata/test_p256n13.rz", "testdata/test_p256n13.dat", 13, 16, 16, FlagPreprocess},
	{"p256n14", "testdata/test_p256n14.rz", "testdata/test_p256n14.dat", 14, 16, 16, FlagPreprocess},
	{"p256n15", "testdata/test_p256n15.rz", "testdata/test_p256n15.dat", 15, 16, 16, FlagPreprocess},
	{"p256n16", "testdata/test_p256n16.rz", "testdata/test_p256n16.dat", 16, 16, 16, FlagPreprocess},
	// 17–32 bit: BlockSize=16, RSI=32 (idLen=5)
	{"p512n17", "testdata/test_p512n17.rz", "testdata/test_p512n17.dat", 17, 16, 32, FlagPreprocess},
	{"p512n20", "testdata/test_p512n20.rz", "testdata/test_p512n20.dat", 20, 16, 32, FlagPreprocess},
	{"p512n24", "testdata/test_p512n24.rz", "testdata/test_p512n24.dat", 24, 16, 32, FlagPreprocess},
	{"p512n28", "testdata/test_p512n28.rz", "testdata/test_p512n28.dat", 28, 16, 32, FlagPreprocess},
	{"p512n32", "testdata/test_p512n32.rz", "testdata/test_p512n32.dat", 32, 16, 32, FlagPreprocess},
}

var lowEntropyVectors = []testVector{
	// Lowset1: BlockSize=16, RSI=64
	{"lowset1-n01-basic", "testdata/Lowset1_8bit.n01-basic.rz", "testdata/Lowset1_8bit.dat", 1, 16, 64, FlagPreprocess},
	{"lowset1-n02-basic", "testdata/Lowset1_8bit.n02-basic.rz", "testdata/Lowset1_8bit.dat", 2, 16, 64, FlagPreprocess},
	{"lowset1-n03-basic", "testdata/Lowset1_8bit.n03-basic.rz", "testdata/Lowset1_8bit.dat", 3, 16, 64, FlagPreprocess},
	{"lowset1-n04-basic", "testdata/Lowset1_8bit.n04-basic.rz", "testdata/Lowset1_8bit.dat", 4, 16, 64, FlagPreprocess},
	// Lowset2: distinct distribution, exercises zero-block runs
	{"lowset2-n01-basic", "testdata/Lowset2_8bit.n01-basic.rz", "testdata/Lowset2_8bit.dat", 1, 16, 64, FlagPreprocess},
	{"lowset2-n02-basic", "testdata/Lowset2_8bit.n02-basic.rz", "testdata/Lowset2_8bit.dat", 2, 16, 64, FlagPreprocess},
	{"lowset2-n05", "testdata/Lowset2_8bit.n05.rz", "testdata/Lowset2_8bit.dat", 5, 16, 64, FlagPreprocess},
	{"lowset2-n08", "testdata/Lowset2_8bit.n08.rz", "testdata/Lowset2_8bit.dat", 8, 16, 64, FlagPreprocess},
	// Lowset3: long zero-block runs, hits ROS=5 fill-to-boundary path
	{"lowset3-n01-basic", "testdata/Lowset3_8bit.n01-basic.rz", "testdata/Lowset3_8bit.dat", 1, 16, 64, FlagPreprocess},
	{"lowset3-n04-basic", "testdata/Lowset3_8bit.n04-basic.rz", "testdata/Lowset3_8bit.dat", 4, 16, 64, FlagPreprocess},
	{"lowset3-n08", "testdata/Lowset3_8bit.n08.rz", "testdata/Lowset3_8bit.dat", 8, 16, 64, FlagPreprocess},
}

var extendedVectors = []testVector{
	// BlockSize=16, RSI=256 — typical SAR
	{"sar32bit-j16-r256", "testdata/sar32bit.j16.r256.rz", "testdata/sar32bit.dat", 32, 16, 256, FlagPreprocess | FlagPadRSI},
	// BlockSize=64, RSI=4096 — large block + large RSI stress test
	{"sar32bit-j64-r4096", "testdata/sar32bit.j64.r4096.rz", "testdata/sar32bit.dat", 32, 64, 4096, FlagPreprocess | FlagPadRSI},
}

// CCSDS 121B2 test vectors from the official specification.
// These validate the decoder against the reference implementation (libaec).
// All vectors pass: 1–32 bit, all coding modes, low entropy, extended parameters.

func TestCCSDS_AllOptions(t *testing.T) {
	for _, tv := range allOptionsVectors {
		t.Run(tv.name, func(t *testing.T) {
			testDecode(t, tv)
		})
	}
}

func TestCCSDS_LowEntropy(t *testing.T) {
	for _, tv := range lowEntropyVectors {
		t.Run(tv.name, func(t *testing.T) {
			testDecode(t, tv)
		})
	}
}

func TestCCSDS_Extended(t *testing.T) {
	for _, tv := range extendedVectors {
		t.Run(tv.name, func(t *testing.T) {
			testDecode(t, tv)
		})
	}
}

func testDecode(t *testing.T, tv testVector) {
	t.Helper()
	compressed := loadTestFile(t, tv.compressed)
	reference := loadTestFile(t, tv.reference)

	nv := numValues(tv.reference, tv.bitsPerSample)
	p := Params{
		BitsPerSample: tv.bitsPerSample,
		BlockSize:     tv.blockSize,
		RSI:           tv.rsi,
		Flags:         tv.flags,
		NumValues:     nv,
	}

	got, err := Decode(compressed, p)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := referenceToUint32(reference, tv.bitsPerSample)
	if len(got) > len(want) {
		got = got[:len(want)]
	}

	mismatches := 0
	for i := range got {
		if got[i] != want[i] {
			if mismatches < 5 {
				t.Errorf("sample[%d]: got %d, want %d", i, got[i], want[i])
			}
			mismatches++
		}
	}
	if mismatches > 5 {
		t.Errorf("... and %d more mismatches", mismatches-5)
	}
	if mismatches == 0 {
		t.Logf("OK: %d samples match", len(got))
	}
}

// --- Real-world regression ---

// TestRealWorldECMWFAIFS decodes a Section-7 AEC payload extracted from a
// real ECMWF AIFS open-data GRIB2 message (template 5.42). The expected
// SHA256 was produced by libaec 1.1.6's aec_buffer_decode on the same
// payload, with output reinterpreted from big-endian uint16 (libaec's
// FlagMSB layout) to little-endian uint32 for hashing.
//
// Parameters extracted from GRIB2 Section 5:
//
//	NumValues=1038240, BitsPerSample=16, BlockSize=32, RSI=128,
//	Flags=0x0e (Flag3Byte | FlagMSB | FlagPreprocess)
//
// This is the only test in the suite that exercises BlockSize=32 and
// Flag3Byte being set on 16-bit data (libaec ignores Flag3Byte unless
// BitsPerSample is 17–24; we must do the same).
func TestRealWorldECMWFAIFS(t *testing.T) {
	const expectedHash = "ac77074c245e5e76573693c5c3482b74af7227140438723c2c2a672dfd521fef"
	compressed := loadTestFile(t, "testdata/ecmwf_aifs_t.aec")
	out, err := Decode(compressed, Params{
		BitsPerSample: 16,
		BlockSize:     32,
		RSI:           128,
		Flags:         Flag3Byte | FlagMSB | FlagPreprocess,
		NumValues:     1038240,
	})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1038240 {
		t.Fatalf("len=%d, want 1038240", len(out))
	}
	buf := make([]byte, 4*len(out))
	for i, v := range out {
		binary.LittleEndian.PutUint32(buf[i*4:], v)
	}
	sum := sha256.Sum256(buf)
	got := hex.EncodeToString(sum[:])
	if got != expectedHash {
		t.Fatalf("output hash mismatch:\n  got  %s\n  want %s", got, expectedHash)
	}
}

// TestDecodeInto verifies that the caller-supplied-buffer entry point yields
// identical output to Decode and reuses the backing array when capacity
// permits, eliminating an allocation on hot paths.
func TestDecodeInto(t *testing.T) {
	compressed := loadTestFile(t, "testdata/test_p256n08.rz")
	p := Params{
		BitsPerSample: 8, BlockSize: 16, RSI: 16,
		Flags: FlagPreprocess, NumValues: 256,
	}
	want, err := Decode(compressed, p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint32, 0, 256)
	got, err := DecodeInto(buf, compressed, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want=%d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("mismatch at %d: got=%d want=%d", i, got[i], want[i])
		}
	}
	// Reuse: pre-dirty the buffer; a second decode must scrub stale entries
	// during zero-block handling and produce the same output.
	dirty := got[:cap(got)]
	for i := range dirty {
		dirty[i] = 0xDEADBEEF
	}
	got2, err := DecodeInto(dirty[:0], compressed, p)
	if err != nil {
		t.Fatal(err)
	}
	for i := range got2 {
		if got2[i] != want[i] {
			t.Fatalf("reuse mismatch at %d: got=%d want=%d", i, got2[i], want[i])
		}
	}
}

// TestReadBitsBoundsCheck locks in the n > 56 guard. Without it the
// (1<<n)-1 mask wraps and the decoder can silently return wrong values.
func TestReadBitsBoundsCheck(t *testing.T) {
	br := &bitReader{data: []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}}
	if v := br.readBits(64); v != 0 {
		t.Errorf("readBits(64) = %d, want 0 (rejected)", v)
	}
	if v := br.readBits(0); v != 0 {
		t.Errorf("readBits(0) = %d, want 0 (rejected)", v)
	}
}

// TestInversePreprocessShortSlice exercises the short-slice guard added so
// callers cannot trip an out-of-bounds read on a degenerate RSI.
func TestInversePreprocessShortSlice(t *testing.T) {
	inversePreprocess(nil, 255)        // must not panic
	inversePreprocess([]uint32{}, 255) // must not panic
	one := []uint32{42}
	inversePreprocess(one, 255)
	if one[0] != 42 {
		t.Errorf("len=1 slice modified: got %d, want 42", one[0])
	}
}

// --- Benchmarks ---

func BenchmarkDecode_8bit(b *testing.B) {
	compressed := loadBenchFile(b, "testdata/test_p256n08.rz")
	p := Params{
		BitsPerSample: 8,
		BlockSize:     16,
		RSI:           16,
		Flags:         FlagPreprocess,
		NumValues:     256,
	}
	b.SetBytes(256)
	b.ResetTimer()
	for b.Loop() {
		Decode(compressed, p)
	}
}

func BenchmarkDecode_32bit_Preprocess(b *testing.B) {
	compressed := loadBenchFile(b, "testdata/sar32bit.j16.r256.rz")
	p := Params{
		BitsPerSample: 32,
		BlockSize:     16,
		RSI:           256,
		Flags:         FlagPreprocess | FlagPadRSI,
		NumValues:     262144, // 1MB / 4 bytes
	}
	b.SetBytes(262144 * 4)
	b.ResetTimer()
	for b.Loop() {
		Decode(compressed, p)
	}
}

// BenchmarkDecodeInto_32bit measures the hot-path win of reusing a
// caller-supplied output buffer across many decodes (matches GRIB2 ingest
// patterns where each message decodes into the same destination).
func BenchmarkDecodeInto_32bit(b *testing.B) {
	compressed := loadBenchFile(b, "testdata/sar32bit.j16.r256.rz")
	p := Params{
		BitsPerSample: 32,
		BlockSize:     16,
		RSI:           256,
		Flags:         FlagPreprocess | FlagPadRSI,
		NumValues:     262144,
	}
	buf := make([]uint32, p.NumValues)
	b.SetBytes(262144 * 4)
	b.ResetTimer()
	for b.Loop() {
		DecodeInto(buf, compressed, p)
	}
}

// BenchmarkDecode_FSHeavy stresses readFS using the Lowset3 vector — its
// dominant codepath is long fundamental-sequence runs, exactly the case the
// fillUpTo(56) prefill is designed for.
func BenchmarkDecode_FSHeavy(b *testing.B) {
	compressed := loadBenchFile(b, "testdata/Lowset3_8bit.n08.rz")
	p := Params{
		BitsPerSample: 8,
		BlockSize:     16,
		RSI:           64,
		Flags:         FlagPreprocess,
		NumValues:     2048,
	}
	b.SetBytes(int64(p.NumValues))
	b.ResetTimer()
	for b.Loop() {
		Decode(compressed, p)
	}
}

// --- Helpers ---

func loadTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("test file unavailable: %s", path)
	}
	return data
}

func loadBenchFile(b *testing.B, path string) []byte {
	b.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		b.Skipf("bench file unavailable: %s", path)
	}
	return data
}

// numValues calculates the number of samples from a .dat file path and bit depth.
// libaec's graec, in default (non-3-byte) mode, packs 17–24-bit samples into
// 4 bytes per sample, not 3. This matches what the CCSDS .dat reference files
// contain.
func numValues(datPath string, bitsPerSample int) int {
	info, err := os.Stat(datPath)
	if err != nil {
		return 0
	}
	bytesPerSample := bytesPerSample(bitsPerSample)
	return int(info.Size()) / bytesPerSample
}

func bytesPerSample(bitsPerSample int) int {
	switch {
	case bitsPerSample <= 8:
		return 1
	case bitsPerSample <= 16:
		return 2
	default:
		return 4 // 17–32 bits, libaec default packing
	}
}

// referenceToUint32 interprets raw little-endian packed bytes as uint32 samples.
// The CCSDS test vectors are produced by libaec's graec tool which defaults to
// LSB (little-endian) byte order for multi-byte samples.
func referenceToUint32(data []byte, bitsPerSample int) []uint32 {
	bps := bytesPerSample(bitsPerSample)
	n := len(data) / bps
	out := make([]uint32, n)
	for i := 0; i < n; i++ {
		off := i * bps
		switch bps {
		case 1:
			out[i] = uint32(data[off])
		case 2:
			out[i] = uint32(binary.LittleEndian.Uint16(data[off:]))
		case 4:
			out[i] = binary.LittleEndian.Uint32(data[off:])
		}
	}
	return out
}
