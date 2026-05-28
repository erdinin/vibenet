package stratum

import (
	"math/big"
)

// diff1Target is the canonical "difficulty 1" target used by Bitcoin pools:
//
//	0x00000000FFFF0000000000000000000000000000000000000000000000000000
//
// Pool target is derived as floor(diff1Target / pool_difficulty).
var diff1Target = func() *big.Int {
	v, _ := new(big.Int).SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)
	return v
}()

// DifficultyToTarget converts a (possibly fractional) pool difficulty into a
// big-endian 32-byte target. A hash is considered a share iff
// hashAsBigEndianUint256 <= target.
func DifficultyToTarget(difficulty float64) [32]byte {
	if difficulty <= 0 {
		difficulty = 1
	}
	maxF := new(big.Float).SetInt(diff1Target)
	diffF := new(big.Float).SetFloat64(difficulty)
	targetF := new(big.Float).Quo(maxF, diffF)
	target, _ := targetF.Int(nil)

	var out [32]byte
	b := target.Bytes()
	if len(b) > 32 {
		b = b[len(b)-32:]
	}
	copy(out[32-len(b):], b)
	return out
}

// HashLEMeetsTarget compares a SHA-256d output (in its native little-endian
// "internal" byte order, as it leaves the SHA engine) against a big-endian
// target. Returns true when the hash, reinterpreted as a big-endian uint256,
// is less than or equal to the target.
func HashLEMeetsTarget(hashLE [32]byte, targetBE [32]byte) bool {
	for i := 0; i < 32; i++ {
		h := hashLE[31-i]
		t := targetBE[i]
		if h < t {
			return true
		}
		if h > t {
			return false
		}
	}
	return true
}
