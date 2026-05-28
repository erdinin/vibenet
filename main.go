// Package main is the VibeNet worker entry point.
//
// VibeNet is a decentralized P2P mining network. This binary spins up a local
// CPU mining worker that benchmarks raw double-SHA256 throughput against a
// random nonce stream. It is intentionally pool-protocol-free so contributors
// can swap in the transport layer (stratum-v2, libp2p, etc.) without touching
// the hashing core.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"vibenet/miner"
)

const banner = "\033[36m" +
	" ██╗   ██╗██╗██████╗ ███████╗███╗   ██╗███████╗████████╗\n" +
	" ██║   ██║██║██╔══██╗██╔════╝████╗  ██║██╔════╝╚══██╔══╝\n" +
	" ██║   ██║██║██████╔╝█████╗  ██╔██╗ ██║█████╗     ██║   \n" +
	" ╚██╗ ██╔╝██║██╔══██╗██╔══╝  ██║╚██╗██║██╔══╝     ██║   \n" +
	"  ╚████╔╝ ██║██████╔╝███████╗██║ ╚████║███████╗   ██║   \n" +
	"   ╚═══╝  ╚═╝╚═════╝ ╚══════╝╚═╝  ╚═══╝╚══════╝   ╚═╝   \n" +
	"\033[0m" +
	"\033[90m   Decentralized P2P Mining Network  ·  Worker Core\033[0m\n"

func main() {
	wallet := flag.String("wallet", "", "Bitcoin wallet address (BTC) to receive payouts")
	worker := flag.String("worker", "rig-01", "Worker name (free-form identifier)")
	threads := flag.Int("threads", runtime.NumCPU(), "Number of worker threads (defaults to all CPU cores)")
	flag.Parse()

	if *wallet == "" {
		fmt.Fprintln(os.Stderr, "error: --wallet is required")
		fmt.Fprintln(os.Stderr, "usage: vibenet --wallet <BTC_ADDRESS> [--worker <NAME>] [--threads <N>]")
		os.Exit(2)
	}

	fmt.Print(banner)
	fmt.Println()
	fmt.Printf("   \033[1mWallet \033[0m  %s\n", *wallet)
	fmt.Printf("   \033[1mWorker \033[0m  %s\n", *worker)
	fmt.Printf("   \033[1mThreads\033[0m  %d\n", *threads)
	fmt.Println("   ────────────────────────────────────────────────────────")

	m := miner.New(miner.Config{
		Wallet:  *wallet,
		Worker:  *worker,
		Threads: *threads,
	})

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Print("\n   \033[33m⛏  stopping workers...\033[0m\n")
		m.Stop()
	}()

	m.Run()
	fmt.Println("   \033[32m✓  miner stopped cleanly.\033[0m")
}
