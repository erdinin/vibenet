// Package miner is the VibeNet hashing engine.
//
// A Miner owns N worker goroutines that double-SHA256 over a block-header
// template received from the stratum layer, iterate the 32-bit nonce, and
// emit any hash that meets the current pool share target. The package is
// transport-agnostic: it talks to the pool via SetJob / SetTarget /
// Shares() rather than embedding the protocol.
package miner

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"vibenet/stratum"
)

// Config holds the runtime parameters for a Miner.
type Config struct {
	Threads int
}

// Miner spins up Threads worker goroutines and emits shares meeting the
// current target on its Shares() channel.
type Miner struct {
	threads int

	job          atomic.Pointer[stratum.Job]
	shareTarget  atomic.Pointer[[32]byte]
	enonce2Seq   atomic.Uint64
	hashes       atomic.Uint64

	shares chan stratum.Share

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// New constructs a Miner; call Run to spawn workers.
func New(cfg Config) *Miner {
	if cfg.Threads < 1 {
		cfg.Threads = 1
	}
	return &Miner{
		threads: cfg.Threads,
		shares:  make(chan stratum.Share, 16),
		stopCh:  make(chan struct{}),
	}
}

// SetJob installs a new job. Workers pick it up at the next batch boundary.
func (m *Miner) SetJob(j *stratum.Job) { m.job.Store(j) }

// SetTarget installs a new pool share target. Workers pick it up at the next
// enonce2 boundary; the same target is captured for the lifetime of one
// (job, enonce2) sweep so a tight inner loop reads a stack-local copy.
func (m *Miner) SetTarget(t [32]byte) { m.shareTarget.Store(&t) }

// Shares is the channel of share candidates. It closes when Run returns.
func (m *Miner) Shares() <-chan stratum.Share { return m.shares }

// Hashes returns the running total of double-SHA256 ops performed by all
// workers since Run started.
func (m *Miner) Hashes() uint64 { return m.hashes.Load() }

// Run spawns the worker goroutines and blocks until Stop is called.
// The shares channel is closed before Run returns.
func (m *Miner) Run() {
	for i := 0; i < m.threads; i++ {
		m.wg.Add(1)
		go m.workerLoop()
	}
	m.wg.Wait()
	close(m.shares)
}

// Stop signals workers to exit at the next batch boundary. Safe to call
// multiple times.
func (m *Miner) Stop() {
	m.stopOnce.Do(func() { close(m.stopCh) })
}

func (m *Miner) workerLoop() {
	defer m.wg.Done()

	for {
		select {
		case <-m.stopCh:
			return
		default:
		}

		j := m.job.Load()
		tp := m.shareTarget.Load()
		if j == nil || tp == nil {
			// Pool hasn't delivered the first job or target yet. Wait briefly.
			select {
			case <-m.stopCh:
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}

		enonce2 := m.nextEnonce2(j.ExtraNonce2Size)
		m.hashSweep(j, *tp, enonce2)
	}
}

// hashSweep iterates the full uint32 nonce space for one (job, enonce2)
// pair, emitting shares meeting target. Returns when Stop fires, the job
// changes, or the nonce space is exhausted.
func (m *Miner) hashSweep(j *stratum.Job, target [32]byte, enonce2 []byte) {
	header := j.HeaderPrefix(enonce2)

	const batch = 4096
	var nonce uint32

	for {
		select {
		case <-m.stopCh:
			return
		default:
		}
		if m.job.Load() != j {
			return
		}

		var i uint32
		for i = 0; i < batch; i++ {
			binary.LittleEndian.PutUint32(header[76:80], nonce)
			first := sha256.Sum256(header[:])
			hash := sha256.Sum256(first[:])

			if stratum.HashLEMeetsTarget(hash, target) {
				m.emitShare(j, enonce2, nonce)
			}

			if nonce == ^uint32(0) {
				m.hashes.Add(uint64(i + 1))
				return // nonce wrapped, get a fresh enonce2
			}
			nonce++
		}
		m.hashes.Add(uint64(batch))
	}
}

func (m *Miner) emitShare(j *stratum.Job, enonce2 []byte, nonce uint32) {
	en2Copy := make([]byte, len(enonce2))
	copy(en2Copy, enonce2)
	select {
	case m.shares <- stratum.Share{
		JobID:       j.ID,
		ExtraNonce2: en2Copy,
		NTime:       j.NTime,
		Nonce:       nonce,
	}:
	default:
		// Channel full: a slow share-submitter is the bottleneck. Drop this
		// share rather than stalling the hot loop. At sane pool difficulties
		// this is unreachable.
	}
}

func (m *Miner) nextEnonce2(size int) []byte {
	v := m.enonce2Seq.Add(1) - 1
	out := make([]byte, size)
	for i := size - 1; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	return out
}

// FormatHashrate renders a H/s rate at a sensible scale (H/s..TH/s).
func FormatHashrate(hps float64) string {
	switch {
	case hps >= 1e12:
		return fmt.Sprintf("%7.2f TH/s", hps/1e12)
	case hps >= 1e9:
		return fmt.Sprintf("%7.2f GH/s", hps/1e9)
	case hps >= 1e6:
		return fmt.Sprintf("%7.2f MH/s", hps/1e6)
	case hps >= 1e3:
		return fmt.Sprintf("%7.2f KH/s", hps/1e3)
	default:
		return fmt.Sprintf("%7.2f  H/s", hps)
	}
}

// FormatNumber prints n with thousands separators.
func FormatNumber(n uint64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	out := make([]byte, 0, len(s)+len(s)/3)
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
