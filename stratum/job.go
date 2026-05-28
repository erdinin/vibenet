package stratum

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// Job is a parsed mining.notify ready for hashing. The MiningNotify type that
// flows out of the protocol layer carries strings (hex on the wire); Job
// holds the same fields in decoded, byte-swapped, header-ready form so the
// hot path performs no parsing per nonce.
type Job struct {
	ID        string
	Version   uint32   // already decoded from BE hex; written little-endian into the header
	PrevHash  [32]byte // already byte-swapped per the Stratum quirk; drops straight into header[4:36]
	NTime     uint32
	NBits     uint32
	CleanJobs bool

	Coinb1       []byte
	Coinb2       []byte
	MerkleBranch [][32]byte

	ExtraNonce1     []byte
	ExtraNonce2Size int
}

// ParseJob converts a MiningNotify + Subscription into a mining-ready Job.
// All hex strings are decoded, all byte-order conversions are performed, and
// fixed-size buffers are allocated once.
func ParseJob(n MiningNotify, sub Subscription) (*Job, error) {
	j := &Job{
		ID:              n.JobID,
		CleanJobs:       n.CleanJobs,
		ExtraNonce1:     sub.ExtraNonce1,
		ExtraNonce2Size: sub.ExtraNonce2Size,
	}

	var err error
	if j.Version, err = parseUint32Hex(n.VersionHex); err != nil {
		return nil, fmt.Errorf("version: %w", err)
	}
	if j.NBits, err = parseUint32Hex(n.NBitsHex); err != nil {
		return nil, fmt.Errorf("nbits: %w", err)
	}
	if j.NTime, err = parseUint32Hex(n.NTimeHex); err != nil {
		return nil, fmt.Errorf("ntime: %w", err)
	}

	prevRaw, err := hex.DecodeString(n.PrevHashHex)
	if err != nil {
		return nil, fmt.Errorf("prevhash: %w", err)
	}
	if len(prevRaw) != 32 {
		return nil, fmt.Errorf("prevhash: expected 32 bytes, got %d", len(prevRaw))
	}
	j.PrevHash = swapPrevHash([32]byte(prevRaw))

	if j.Coinb1, err = hex.DecodeString(n.Coinb1Hex); err != nil {
		return nil, fmt.Errorf("coinb1: %w", err)
	}
	if j.Coinb2, err = hex.DecodeString(n.Coinb2Hex); err != nil {
		return nil, fmt.Errorf("coinb2: %w", err)
	}

	j.MerkleBranch = make([][32]byte, len(n.MerkleBranch))
	for i, h := range n.MerkleBranch {
		b, err := hex.DecodeString(h)
		if err != nil {
			return nil, fmt.Errorf("merkle_branch[%d]: %w", i, err)
		}
		if len(b) != 32 {
			return nil, fmt.Errorf("merkle_branch[%d]: expected 32 bytes, got %d", i, len(b))
		}
		copy(j.MerkleBranch[i][:], b)
	}

	return j, nil
}

// CoinbaseHash returns SHA256d(coinb1 || extranonce1 || extranonce2 || coinb2).
// The result is in SHA-256 native byte order, ready to feed into MerkleRoot.
func (j *Job) CoinbaseHash(enonce2 []byte) [32]byte {
	h := sha256.New()
	h.Write(j.Coinb1)
	h.Write(j.ExtraNonce1)
	h.Write(enonce2)
	h.Write(j.Coinb2)
	first := h.Sum(nil)
	return sha256.Sum256(first)
}

// MerkleRoot computes the merkle root by folding the coinbase hash up through
// the merkle branch with the standard Bitcoin double-SHA256 combine.
//
//	root = SHA256d(coinbase)
//	for each branch in branches:
//	    root = SHA256d(root || branch)
func (j *Job) MerkleRoot(enonce2 []byte) [32]byte {
	root := j.CoinbaseHash(enonce2)
	var buf [64]byte
	for _, branch := range j.MerkleBranch {
		copy(buf[:32], root[:])
		copy(buf[32:], branch[:])
		first := sha256.Sum256(buf[:])
		root = sha256.Sum256(first[:])
	}
	return root
}

// HeaderPrefix builds the 80-byte block header for the given extranonce2 with
// bytes [76:80] (nonce) left zero. The miner's inner loop overwrites those
// four bytes with each candidate nonce and double-SHA256s the full header.
func (j *Job) HeaderPrefix(enonce2 []byte) [80]byte {
	var hdr [80]byte
	binary.LittleEndian.PutUint32(hdr[0:4], j.Version)
	copy(hdr[4:36], j.PrevHash[:])
	merkle := j.MerkleRoot(enonce2)
	copy(hdr[36:68], merkle[:])
	binary.LittleEndian.PutUint32(hdr[68:72], j.NTime)
	binary.LittleEndian.PutUint32(hdr[72:76], j.NBits)
	return hdr
}

// swapPrevHash reverses each 4-byte word of the prevhash that Stratum sends.
// Stratum delivers prevhash as eight 32-bit words in network (big-endian)
// byte order; the block header expects every word in little-endian order, so
// each 4-byte chunk is reversed in place.
//
// This is the most common source of "all my shares are rejected with stale
// or invalid prevhash" bugs in third-party Stratum clients.
func swapPrevHash(in [32]byte) [32]byte {
	var out [32]byte
	for i := 0; i < 32; i += 4 {
		out[i+0] = in[i+3]
		out[i+1] = in[i+2]
		out[i+2] = in[i+1]
		out[i+3] = in[i+0]
	}
	return out
}

func parseUint32Hex(s string) (uint32, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return 0, err
	}
	if len(b) != 4 {
		return 0, fmt.Errorf("expected 4-byte hex, got %d bytes", len(b))
	}
	return binary.BigEndian.Uint32(b), nil
}
