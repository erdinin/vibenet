package stratum

import (
	"encoding/hex"
	"testing"
)

func TestDifficultyToTarget_Diff1(t *testing.T) {
	got := DifficultyToTarget(1.0)
	want, _ := hex.DecodeString("00000000FFFF0000000000000000000000000000000000000000000000000000")
	for i := 0; i < 32; i++ {
		if got[i] != want[i] {
			t.Fatalf("diff=1 byte %d: got %02x want %02x\nfull got:  %x\nfull want: %x", i, got[i], want[i], got[:], want)
		}
	}
}

func TestDifficultyToTarget_Diff2(t *testing.T) {
	got := DifficultyToTarget(2.0)
	want, _ := hex.DecodeString("000000007FFF8000000000000000000000000000000000000000000000000000")
	for i := 0; i < 32; i++ {
		if got[i] != want[i] {
			t.Fatalf("diff=2 byte %d: got %02x want %02x\nfull got:  %x\nfull want: %x", i, got[i], want[i], got[:], want)
		}
	}
}

func TestDifficultyToTarget_FractionalDiff(t *testing.T) {
	// diff=0.5 should give 2x the diff1 target.
	got := DifficultyToTarget(0.5)
	want, _ := hex.DecodeString("00000001FFFE0000000000000000000000000000000000000000000000000000")
	for i := 0; i < 32; i++ {
		if got[i] != want[i] {
			t.Fatalf("diff=0.5 byte %d: got %02x want %02x\nfull got:  %x\nfull want: %x", i, got[i], want[i], got[:], want)
		}
	}
}

func TestDifficultyToTarget_NonPositiveFallback(t *testing.T) {
	zero := DifficultyToTarget(0)
	neg := DifficultyToTarget(-5)
	one := DifficultyToTarget(1.0)
	if zero != one || neg != one {
		t.Fatalf("non-positive difficulty should fall back to 1; got zero=%x neg=%x one=%x", zero, neg, one)
	}
}

func TestHashLEMeetsTarget_MaxTarget(t *testing.T) {
	var maxTarget [32]byte
	for i := range maxTarget {
		maxTarget[i] = 0xff
	}
	var hash [32]byte
	hash[0] = 0x80
	if !HashLEMeetsTarget(hash, maxTarget) {
		t.Fatal("any hash should meet 0xff..ff target")
	}
}

func TestHashLEMeetsTarget_ZeroTargetEquality(t *testing.T) {
	var zeroTarget [32]byte
	var zeroHash [32]byte
	if !HashLEMeetsTarget(zeroHash, zeroTarget) {
		t.Fatal("zero hash should meet zero target (equality case)")
	}
}

func TestHashLEMeetsTarget_NonZeroAgainstZero(t *testing.T) {
	var zeroTarget [32]byte
	var hash [32]byte
	hash[0] = 0x01 // sha256 LE: byte 0 = high byte when displayed
	if HashLEMeetsTarget(hash, zeroTarget) {
		t.Fatal("non-zero hash must not meet zero target")
	}
}

func TestHashLEMeetsTarget_BitcoinDisplayConvention(t *testing.T) {
	// Bitcoin convention: a "valid" hash has leading zeros when displayed.
	// The displayed hash is reverse(sha256d_output).
	// So sha256d_output ending in 0x00...00 (low bytes of LE) corresponds to a
	// displayed hash starting with 0x00...00 — which beats a small target.
	var hashLE [32]byte
	// Construct an LE hash whose displayed form is 0x00000000_FFFF_0000... (== diff1 target)
	displayed, _ := hex.DecodeString("00000000FFFF0000000000000000000000000000000000000000000000000000")
	for i := 0; i < 32; i++ {
		hashLE[i] = displayed[31-i]
	}
	target := DifficultyToTarget(1.0)
	if !HashLEMeetsTarget(hashLE, target) {
		t.Fatalf("hash equal to diff-1 target must meet target (equality)\nhashLE: %x\ntarget: %x", hashLE, target)
	}

	// Now make the displayed hash one bit bigger — must fail.
	displayed[5] = 0xFF
	displayed[6] = 0x01 // 0x00000000FFFF01... > target
	for i := 0; i < 32; i++ {
		hashLE[i] = displayed[31-i]
	}
	if HashLEMeetsTarget(hashLE, target) {
		t.Fatal("hash strictly greater than target must not meet target")
	}
}
