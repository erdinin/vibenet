// Package miner implements the VibeNet CPU mining core.
//
// The Miner spins up N worker goroutines that compute double-SHA256 over an
// 80-byte header (Bitcoin-shaped: 76-byte prefix + 4-byte nonce) using a
// per-worker random seed, and a single reporter goroutine that prints the
// live hashrate once per second.
//
// The package exposes no network or pool transport on purpose — it is the
// hashing primitive that the rest of VibeNet (P2P layer, share submission,
// payout accounting) is built on top of.
package miner

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Config holds the runtime parameters for a Miner.
type Config struct {
	Wallet  string
	Worker  string
	Threads int
}

// Miner is a CPU SHA-256d benchmark worker.
type Miner struct {
	cfg      Config
	hashes   atomic.Uint64
	stopCh   chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// New returns a Miner ready to Run.
func New(cfg Config) *Miner {
	if cfg.Threads < 1 {
		cfg.Threads = 1
	}
	return &Miner{
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
}

// Run starts all worker goroutines plus the reporter and blocks until Stop is called.
func (m *Miner) Run() {
	for i := 0; i < m.cfg.Threads; i++ {
		m.wg.Add(1)
		go m.workerLoop(i)
	}
	m.wg.Add(1)
	go m.reporter()
	m.wg.Wait()
}

// Stop signals all goroutines to exit. Safe to call multiple times.
func (m *Miner) Stop() {
	m.stopOnce.Do(func() { close(m.stopCh) })
}

// workerLoop is the hot path: tight SHA-256d loop over an incrementing nonce.
//
// We batch hashes between atomic adds so the counter is not the bottleneck.
// Real pool integration would replace `header` with the current job template
// and break out of the inner loop when target difficulty is met.
func (m *Miner) workerLoop(id int) {
	defer m.wg.Done()

	// 80-byte block-header-shaped buffer. Bytes 0..75 hold the (mock) prefix,
	// bytes 76..79 hold the nonce in little-endian — same layout as Bitcoin.
	var header [80]byte
	if _, err := rand.Read(header[:76]); err != nil {
		// Fallback: derive a seed from time + worker id. Good enough for
		// benchmarking; never used in real mining.
		binary.LittleEndian.PutUint64(header[0:8], uint64(time.Now().UnixNano()))
		binary.LittleEndian.PutUint64(header[8:16], uint64(id))
	}

	var nonceSeed [4]byte
	_, _ = rand.Read(nonceSeed[:])
	nonce := binary.LittleEndian.Uint32(nonceSeed[:])

	const batch = 4096
	for {
		select {
		case <-m.stopCh:
			return
		default:
		}

		for i := 0; i < batch; i++ {
			binary.LittleEndian.PutUint32(header[76:80], nonce)
			first := sha256.Sum256(header[:])
			_ = sha256.Sum256(first[:]) // SHA-256d: second pass over the first digest
			nonce++
		}
		m.hashes.Add(batch)
	}
}

// reporter prints a single status line per second and a summary on shutdown.
func (m *Miner) reporter() {
	defer m.wg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	start := time.Now()
	var lastHashes uint64
	lastTime := start

	for {
		select {
		case <-m.stopCh:
			m.printSummary(start)
			return

		case now := <-ticker.C:
			cur := m.hashes.Load()
			delta := cur - lastHashes
			dt := now.Sub(lastTime).Seconds()
			rate := float64(delta) / dt
			uptime := time.Since(start).Round(time.Second)

			// \r overwrites the same terminal line for a clean live readout.
			// Padding spaces at the end clear any leftover characters from
			// the previous, longer line.
			fmt.Printf("\r   \033[36m⛏\033[0m  [\033[1m%s\033[0m]  \033[32m%s\033[0m  │  total \033[90m%s\033[0m  │  up \033[90m%s\033[0m     ",
				m.cfg.Worker,
				formatHashrate(rate),
				formatNumber(cur),
				uptime,
			)

			lastHashes = cur
			lastTime = now
		}
	}
}

func (m *Miner) printSummary(start time.Time) {
	total := m.hashes.Load()
	elapsed := time.Since(start)
	avg := float64(total) / elapsed.Seconds()
	fmt.Println()
	fmt.Println("   ────────────────────────────────────────────────────────")
	fmt.Printf("   total hashes : %s\n", formatNumber(total))
	fmt.Printf("   avg hashrate : %s\n", formatHashrate(avg))
	fmt.Printf("   elapsed      : %s\n", elapsed.Round(time.Second))
	fmt.Printf("   wallet       : %s\n", m.cfg.Wallet)
}

// formatHashrate renders a H/s rate at a sensible scale.
func formatHashrate(hps float64) string {
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

// formatNumber prints n with thousands separators.
func formatNumber(n uint64) string {
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
