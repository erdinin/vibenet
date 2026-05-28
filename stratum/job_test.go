package stratum

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestSwapPrevHash_PerWordReverse(t *testing.T) {
	var in [32]byte
	// Set each of the 8 words to a distinct big-endian pattern, then verify
	// each chunk's bytes have been reversed.
	for i := 0; i < 8; i++ {
		in[i*4+0] = byte(i)*4 + 1
		in[i*4+1] = byte(i)*4 + 2
		in[i*4+2] = byte(i)*4 + 3
		in[i*4+3] = byte(i)*4 + 4
	}
	out := swapPrevHash(in)
	for i := 0; i < 8; i++ {
		if out[i*4+0] != byte(i)*4+4 ||
			out[i*4+1] != byte(i)*4+3 ||
			out[i*4+2] != byte(i)*4+2 ||
			out[i*4+3] != byte(i)*4+1 {
			t.Fatalf("word %d not reversed: %02x %02x %02x %02x",
				i, out[i*4+0], out[i*4+1], out[i*4+2], out[i*4+3])
		}
	}
}

func TestParseUint32Hex(t *testing.T) {
	cases := []struct {
		in   string
		want uint32
	}{
		{"00000000", 0},
		{"00000001", 1},
		{"20000000", 0x20000000},
		{"1d00ffff", 0x1d00ffff},
		{"ffffffff", 0xFFFFFFFF},
	}
	for _, tc := range cases {
		got, err := parseUint32Hex(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %x, want %x", tc.in, got, tc.want)
		}
	}
}

func TestParseUint32Hex_Errors(t *testing.T) {
	if _, err := parseUint32Hex("nothex!"); err == nil {
		t.Error("expected error for non-hex input")
	}
	if _, err := parseUint32Hex("abcd"); err == nil {
		t.Error("expected error for short hex (2 bytes)")
	}
	if _, err := parseUint32Hex("aabbccddee"); err == nil {
		t.Error("expected error for long hex (5 bytes)")
	}
}

func TestCoinbaseHash_EmptyVector(t *testing.T) {
	// SHA-256("") = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	// SHA-256(SHA-256("")) = 5df6e0e2761359d30a8275058e299fcc0381534545f55cf43e41983f5d4c9456
	// (Known double-SHA-256 of the empty string.)
	j := &Job{}
	got := j.CoinbaseHash(nil)
	want, _ := hex.DecodeString("5df6e0e2761359d30a8275058e299fcc0381534545f55cf43e41983f5d4c9456")
	for i := 0; i < 32; i++ {
		if got[i] != want[i] {
			t.Fatalf("byte %d: got %02x, want %02x\nfull got:  %x\nfull want: %x",
				i, got[i], want[i], got[:], want)
		}
	}
}

func TestCoinbaseHash_ConcatenationOrder(t *testing.T) {
	// CoinbaseHash(enonce2) MUST be SHA256d(coinb1 || enonce1 || enonce2 || coinb2).
	// Verify the concatenation order by comparing against an inline build.
	j := &Job{
		Coinb1:      []byte{0x01, 0x02},
		ExtraNonce1: []byte{0xaa, 0xbb},
		Coinb2:      []byte{0x99, 0x88},
	}
	enonce2 := []byte{0xcc, 0xdd}
	got := j.CoinbaseHash(enonce2)

	// Reference build:
	concat := []byte{0x01, 0x02, 0xaa, 0xbb, 0xcc, 0xdd, 0x99, 0x88}
	want := doubleSha256(concat)
	if got != want {
		t.Fatalf("concatenation order wrong:\n got:  %x\n want: %x", got, want)
	}
}

func TestMerkleRoot_EmptyBranch(t *testing.T) {
	// With an empty merkle branch, MerkleRoot == CoinbaseHash.
	j := &Job{
		Coinb1:      []byte{0x01},
		ExtraNonce1: []byte{0x02},
		Coinb2:      []byte{0x03},
	}
	enonce2 := []byte{0x04}
	if j.MerkleRoot(enonce2) != j.CoinbaseHash(enonce2) {
		t.Fatal("empty branch must leave coinbase hash unchanged")
	}
}

func TestMerkleRoot_OneBranch(t *testing.T) {
	// With one branch, root = SHA256d(coinbase_hash || branch).
	j := &Job{
		Coinb1:       []byte{0x01},
		ExtraNonce1:  []byte{0x02},
		Coinb2:       []byte{0x03},
		MerkleBranch: [][32]byte{{0x77}}, // {0x77, 0x00, 0x00, ...}
	}
	enonce2 := []byte{0x04}

	coinbase := j.CoinbaseHash(enonce2)
	var combined [64]byte
	copy(combined[:32], coinbase[:])
	combined[32] = 0x77
	expected := doubleSha256(combined[:])

	if j.MerkleRoot(enonce2) != expected {
		t.Fatal("one-branch merkle root mismatch")
	}
}

func TestParseJob_HeaderLayout(t *testing.T) {
	notify := MiningNotify{
		JobID:        "deadbeef",
		PrevHashHex:  "1122334455667788aabbccddeeff0011223344556677889900aabbccddeeff00",
		Coinb1Hex:    "01000000",
		Coinb2Hex:    "ffffffff",
		MerkleBranch: []string{},
		VersionHex:   "20000000",
		NBitsHex:     "1d00ffff",
		NTimeHex:     "60db8400",
		CleanJobs:    true,
	}
	sub := Subscription{
		ExtraNonce1:     []byte{0xaa, 0xbb},
		ExtraNonce2Size: 4,
	}

	job, err := ParseJob(notify, sub)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != "deadbeef" {
		t.Errorf("id: got %q", job.ID)
	}
	if job.Version != 0x20000000 {
		t.Errorf("version: got %x", job.Version)
	}
	if job.NBits != 0x1d00ffff {
		t.Errorf("nbits: got %x", job.NBits)
	}
	if job.NTime != 0x60db8400 {
		t.Errorf("ntime: got %x", job.NTime)
	}

	// PrevHash[0..4] should be reversed of first 4 hex chars: "11223344" -> 44 33 22 11
	if job.PrevHash[0] != 0x44 || job.PrevHash[1] != 0x33 ||
		job.PrevHash[2] != 0x22 || job.PrevHash[3] != 0x11 {
		t.Errorf("prevhash byte-swap first word wrong: %x", job.PrevHash[:4])
	}

	enonce2 := []byte{0xCC, 0xDD, 0xEE, 0xFF}
	hdr := job.HeaderPrefix(enonce2)

	if got := binary.LittleEndian.Uint32(hdr[0:4]); got != 0x20000000 {
		t.Errorf("header version: got %x", got)
	}
	for i := 0; i < 32; i++ {
		if hdr[4+i] != job.PrevHash[i] {
			t.Errorf("prevhash byte %d: got %x, want %x", i, hdr[4+i], job.PrevHash[i])
		}
	}
	if got := binary.LittleEndian.Uint32(hdr[68:72]); got != 0x60db8400 {
		t.Errorf("header ntime: got %x", got)
	}
	if got := binary.LittleEndian.Uint32(hdr[72:76]); got != 0x1d00ffff {
		t.Errorf("header nbits: got %x", got)
	}
	if hdr[76]|hdr[77]|hdr[78]|hdr[79] != 0 {
		t.Errorf("nonce slot must be zero, got %x", hdr[76:80])
	}
}

func TestParseJob_RejectsBadPrevhash(t *testing.T) {
	notify := MiningNotify{
		PrevHashHex: "deadbeef", // too short
		Coinb1Hex:   "",
		Coinb2Hex:   "",
		VersionHex:  "20000000",
		NBitsHex:    "1d00ffff",
		NTimeHex:    "60db8400",
	}
	if _, err := ParseJob(notify, Subscription{}); err == nil {
		t.Fatal("expected error for too-short prevhash")
	}
}

func TestParseJob_RejectsBadMerkleBranch(t *testing.T) {
	notify := MiningNotify{
		PrevHashHex:  "0000000000000000000000000000000000000000000000000000000000000000",
		Coinb1Hex:    "",
		Coinb2Hex:    "",
		VersionHex:   "20000000",
		NBitsHex:     "1d00ffff",
		NTimeHex:     "60db8400",
		MerkleBranch: []string{"abcd"}, // not 32 bytes
	}
	if _, err := ParseJob(notify, Subscription{}); err == nil {
		t.Fatal("expected error for short merkle branch entry")
	}
}

// doubleSha256 is a test helper; the production path inlines sha256.Sum256
// twice.
func doubleSha256(b []byte) [32]byte {
	first := sha256.Sum256(b)
	return sha256.Sum256(first[:])
}
