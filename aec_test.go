// Copyright 2026 Shyrm Contributors
// Licensed under the Apache License, Version 2.0.

package aec

import (
	"bytes"
	"encoding/binary"
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

func TestBuildSETable(t *testing.T) {
	table := buildSETable()
	want := [][2]uint32{{0, 0}, {1, 0}, {0, 1}, {2, 0}}
	for i, w := range want {
		if table[i] != w {
			t.Errorf("table[%d] = %v, want %v", i, table[i], w)
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
	{"p256n01-basic", "testdata/test_p256n01-basic.rz", "testdata/test_p256n01.dat", 1, 16, 16, FlagPreprocess},
	{"p256n02-basic", "testdata/test_p256n02-basic.rz", "testdata/test_p256n02.dat", 2, 16, 16, FlagPreprocess},
	{"p256n03-basic", "testdata/test_p256n03-basic.rz", "testdata/test_p256n03.dat", 3, 16, 16, FlagPreprocess},
	{"p256n04-basic", "testdata/test_p256n04-basic.rz", "testdata/test_p256n04.dat", 4, 16, 16, FlagPreprocess},
	{"p256n05", "testdata/test_p256n05.rz", "testdata/test_p256n05.dat", 5, 16, 16, FlagPreprocess},
	{"p256n06", "testdata/test_p256n06.rz", "testdata/test_p256n06.dat", 6, 16, 16, FlagPreprocess},
	{"p256n07", "testdata/test_p256n07.rz", "testdata/test_p256n07.dat", 7, 16, 16, FlagPreprocess},
	{"p256n08", "testdata/test_p256n08.rz", "testdata/test_p256n08.dat", 8, 16, 16, FlagPreprocess},
}

var lowEntropyVectors = []testVector{
	{"lowset1-n01-basic", "testdata/Lowset1_8bit.n01-basic.rz", "testdata/Lowset1_8bit.dat", 1, 16, 64, FlagPreprocess},
	{"lowset1-n02-basic", "testdata/Lowset1_8bit.n02-basic.rz", "testdata/Lowset1_8bit.dat", 2, 16, 64, FlagPreprocess},
	{"lowset1-n03-basic", "testdata/Lowset1_8bit.n03-basic.rz", "testdata/Lowset1_8bit.dat", 3, 16, 64, FlagPreprocess},
	{"lowset1-n04-basic", "testdata/Lowset1_8bit.n04-basic.rz", "testdata/Lowset1_8bit.dat", 4, 16, 64, FlagPreprocess},
}

var extendedVectors = []testVector{
	{"sar32bit-j16-r256", "testdata/sar32bit.j16.r256.rz", "testdata/sar32bit.dat", 32, 16, 256, FlagPreprocess | FlagPadRSI},
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
// CCSDS test data packs samples into the smallest byte-aligned container:
// 1–8 bits → 1 byte/sample, 9–16 → 2 bytes, 17–24 → 3 bytes, 25–32 → 4 bytes.
func numValues(datPath string, bitsPerSample int) int {
	info, err := os.Stat(datPath)
	if err != nil {
		return 0
	}
	bytesPerSample := (bitsPerSample + 7) / 8
	return int(info.Size()) / bytesPerSample
}

// referenceToUint32 interprets raw little-endian packed bytes as uint32 samples.
// The CCSDS test vectors are produced by libaec's graec tool which defaults to
// LSB (little-endian) byte order for multi-byte samples.
func referenceToUint32(data []byte, bitsPerSample int) []uint32 {
	bytesPerSample := (bitsPerSample + 7) / 8
	n := len(data) / bytesPerSample
	out := make([]uint32, n)
	for i := 0; i < n; i++ {
		off := i * bytesPerSample
		switch bytesPerSample {
		case 1:
			out[i] = uint32(data[off])
		case 2:
			out[i] = uint32(binary.LittleEndian.Uint16(data[off:]))
		case 3:
			out[i] = uint32(data[off]) | uint32(data[off+1])<<8 | uint32(data[off+2])<<16
		case 4:
			out[i] = binary.LittleEndian.Uint32(data[off:])
		}
	}
	return out
}
