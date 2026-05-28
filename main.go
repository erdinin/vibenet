// Package main is the VibeNet worker entry point.
//
// vibenet --wallet bc1q... [--worker rig-01] [--threads N] [--pool host:port]
//
// Dials a Stratum v1 solo pool (default: solo.ckpool.org:3333), authorises
// with `<wallet>.<worker>` as the username, mines double-SHA256 against the
// pool's job stream, and submits any share that meets the pool target. Live
// hashrate, accept/reject counters and pool difficulty are printed once per
// second to the terminal.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"vibenet/miner"
	"vibenet/pair"
	"vibenet/stratum"
)

const (
	defaultPool  = "solo.ckpool.org:3333"
	userAgent    = "VibeNet/0.1.0"
	stratumPass  = "x"
)

const banner = "\033[36m" +
	" ██╗   ██╗██╗██████╗ ███████╗███╗   ██╗███████╗████████╗\n" +
	" ██║   ██║██║██╔══██╗██╔════╝████╗  ██║██╔════╝╚══██╔══╝\n" +
	" ██║   ██║██║██████╔╝█████╗  ██╔██╗ ██║█████╗     ██║   \n" +
	" ╚██╗ ██╔╝██║██╔══██╗██╔══╝  ██║╚██╗██║██╔══╝     ██║   \n" +
	"  ╚████╔╝ ██║██████╔╝███████╗██║ ╚████║███████╗   ██║   \n" +
	"   ╚═══╝  ╚═╝╚═════╝ ╚══════╝╚═╝  ╚═══╝╚══════╝   ╚═╝   \n" +
	"\033[0m" +
	"\033[90m   CPU Bitcoin solo miner  ·  Stratum v1  ·  open source\033[0m\n"

func main() {
	wallet := flag.String("wallet", "", "Bitcoin wallet address (BTC) to receive block payouts")
	worker := flag.String("worker", "rig-01", "Worker name (free-form identifier)")
	threads := flag.Int("threads", runtime.NumCPU(), "Number of mining goroutines")
	pool := flag.String("pool", defaultPool, "Stratum endpoint (host:port)")
	mode := flag.String("mode", "auto", "Mining gate: auto (pair with active dev tools) | always | off")
	flag.Parse()

	switch *mode {
	case "auto", "always", "off":
	default:
		fmt.Fprintf(os.Stderr, "error: --mode must be auto, always, or off (got %q)\n", *mode)
		os.Exit(2)
	}

	if *wallet == "" {
		fmt.Fprintln(os.Stderr, "error: --wallet is required")
		fmt.Fprintln(os.Stderr, "usage: vibenet --wallet <BTC_ADDRESS> [--worker <NAME>] [--threads <N>] [--pool <HOST:PORT>]")
		os.Exit(2)
	}

	fmt.Print(banner)
	fmt.Println()
	fmt.Printf("   \033[1mWallet \033[0m  %s\n", *wallet)
	fmt.Printf("   \033[1mWorker \033[0m  %s\n", *worker)
	fmt.Printf("   \033[1mThreads\033[0m  %d\n", *threads)
	fmt.Printf("   \033[1mPool   \033[0m  %s\n", *pool)
	fmt.Printf("   \033[1mMode   \033[0m  %s\n", *mode)
	fmt.Println("   ────────────────────────────────────────────────────────")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	user := *wallet + "." + *worker

	fmt.Printf("   \033[36m⛏\033[0m  connecting to %s ...\n", *pool)
	client, err := stratum.Dial(ctx, *pool, user, stratumPass, userAgent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "   \033[31m✗\033[0m  %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	m := miner.New(miner.Config{Threads: *threads})

	var (
		accepted atomic.Uint64
		rejected atomic.Uint64
	)

	// Client read loop — exits when context cancels or connection drops.
	clientErrCh := make(chan error, 1)
	go func() { clientErrCh <- client.Run(ctx) }()

	// Translate notify -> Job -> miner.
	go dispatchNotify(ctx, client, m)

	// Translate set_difficulty -> target -> miner.
	go dispatchDifficulty(ctx, client, m)

	// Pull shares from the miner, submit to the pool.
	go submitShares(ctx, client, m, user, &accepted, &rejected)

	// Mode-driven gate: pair detection feeds m.SetEnabled().
	var pairStatus atomic.Pointer[string]
	go gateMiner(ctx, m, *mode, &pairStatus)

	// Live status reporter.
	go report(ctx, m, *worker, *mode, client, &accepted, &rejected, &pairStatus)

	// Drive the miner.
	minerDone := make(chan struct{})
	go func() { m.Run(); close(minerDone) }()

	// Wait for either context cancellation or fatal stratum error.
	select {
	case <-ctx.Done():
		fmt.Print("\n   \033[33m⛏\033[0m  shutting down...\n")
	case err := <-clientErrCh:
		if err != nil && ctx.Err() == nil {
			fmt.Printf("\n   \033[31m✗\033[0m  stratum: %v\n", err)
		}
		cancel()
	}

	m.Stop()
	client.Close()
	<-minerDone
	fmt.Println("   \033[32m✓\033[0m  stopped cleanly.")
}

func dispatchNotify(ctx context.Context, c *stratum.Client, m *miner.Miner) {
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-c.Notifications():
			if !ok {
				return
			}
			sub := c.Subscription()
			if sub == nil {
				continue
			}
			j, err := stratum.ParseJob(n, *sub)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\n   \033[31m✗\033[0m  parse job %s: %v\n", n.JobID, err)
				continue
			}
			m.SetJob(j)
		}
	}
}

func dispatchDifficulty(ctx context.Context, c *stratum.Client, m *miner.Miner) {
	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-c.Difficulty():
			if !ok {
				return
			}
			m.SetTarget(stratum.DifficultyToTarget(d))
		}
	}
}

func submitShares(ctx context.Context, c *stratum.Client, m *miner.Miner, user string, accepted, rejected *atomic.Uint64) {
	for s := range m.Shares() {
		ok, reason, err := c.Submit(ctx, user, s)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			rejected.Add(1)
			fmt.Fprintf(os.Stderr, "\n   \033[31m✗\033[0m  submit: %v\n", err)
			continue
		}
		if ok {
			n := accepted.Add(1)
			fmt.Printf("\n   \033[32m✓\033[0m  share accepted  (#%d)\n", n)
		} else {
			rejected.Add(1)
			fmt.Printf("\n   \033[33m!\033[0m  share rejected: %s\n", reason)
		}
	}
}

// gateMiner controls the miner's hashing gate according to --mode:
//   - always: enabled forever
//   - off:    disabled forever
//   - auto:   polls pair detection every 5 s; enabled iff a dev tool is running
func gateMiner(ctx context.Context, m *miner.Miner, mode string, status *atomic.Pointer[string]) {
	switch mode {
	case "always":
		m.SetEnabled(true)
		setPairStatus(status, "always-on")
		return
	case "off":
		m.SetEnabled(false)
		setPairStatus(status, "paused (mode=off)")
		return
	}

	det := pair.New(pair.LoadUserOverrides())
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	check := func() {
		paired, matched, err := det.IsPaired()
		if err != nil {
			setPairStatus(status, "pair check failed: "+err.Error())
			m.SetEnabled(false)
			return
		}
		if paired {
			m.SetEnabled(true)
			setPairStatus(status, "paired with "+strings.Join(matched, ", "))
		} else {
			m.SetEnabled(false)
			setPairStatus(status, "sleeping (no dev tools)")
		}
	}

	check() // immediate first poll so the first report tick has fresh data
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func setPairStatus(p *atomic.Pointer[string], s string) { p.Store(&s) }

func report(ctx context.Context, m *miner.Miner, worker, mode string, c *stratum.Client, accepted, rejected *atomic.Uint64, pairStatus *atomic.Pointer[string]) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	start := time.Now()
	lastHashes := uint64(0)
	lastTime := start

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			cur := m.Hashes()
			rate := float64(cur-lastHashes) / now.Sub(lastTime).Seconds()
			uptime := time.Since(start).Round(time.Second)

			status := ""
			if p := pairStatus.Load(); p != nil {
				status = *p
			}

			fmt.Printf("\r   \033[36m⛏\033[0m  [\033[1m%s\033[0m]  \033[32m%s\033[0m  │  shares \033[1m%d\033[0m/\033[90m%d\033[0m  │  diff \033[90m%.3f\033[0m  │  up \033[90m%s\033[0m  │  \033[90m%s\033[0m     ",
				worker,
				miner.FormatHashrate(rate),
				accepted.Load(),
				rejected.Load(),
				c.CurrentDifficulty(),
				uptime,
				status,
			)

			lastHashes = cur
			lastTime = now
		}
	}
}
