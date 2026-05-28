# VibeNet

A CPU-based Bitcoin solo miner with idle-priority workers and optional auto-pairing to active development sessions. Single static binary. Stratum v1. No custody.

> **Status:** v0.1.0-alpha. End-to-end CPU solo mining works against `solo.ckpool.org` — Stratum v1 client, job parsing, header construction and share submission are in place. Auto-pairing to development sessions and signed release binaries are next. See [Roadmap](#roadmap) for the current state.

## What it does

VibeNet spawns `N` worker goroutines that compute double-SHA256 over Bitcoin block headers received from a [solo pool](https://solo.ckpool.org/). If one of your workers produces a hash that meets the network target, the pool publishes the block and the entire coinbase reward (currently **3.125 BTC**, post-2024 halving) is paid directly to the address you specified, minus the operator's 2 % solo fee.

This is genuine solo mining. Your hashpower is not pooled with anyone else's; only **your** share, on **your** worker, can earn the block. There is no custody, no account, no KYC. The pool address you configure is the only place coinbase outputs can land.

## Quick start

Download the binary for your OS/arch from the [Releases page](https://github.com/erdinin/vibenet/releases) (Linux / macOS / Windows · amd64 + arm64) and run:

```sh
./vibenet --wallet bc1q...your-btc-address
```

That's it. No account, no signup, no config file. The Stratum username sent to the pool is `<wallet>.<worker>`; payouts go directly to `<wallet>`.

**Required:**

| Flag | Description |
| --- | --- |
| `--wallet <BTC_ADDRESS>` | Destination for any block payout. SegWit (`bc1q…`), Taproot (`bc1p…`), and legacy addresses accepted. |

**Optional:**

| Flag | Default | Description |
| --- | --- | --- |
| `--worker <name>` | `rig-01` | Free-form worker identifier sent to the pool. |
| `--threads <N>` | all CPU cores | Number of mining goroutines. |
| `--pool <host:port>` | `solo.ckpool.org:3333` | Stratum endpoint. Any solo-pool that speaks Stratum v1 should work. |
| `--mode <auto\|always\|off>` | `auto` | See [Modes](#modes). |
| `--intensity <low\|medium\|high>` | `low` | Process priority class of the worker goroutines. |

## Modes

| Mode | Behavior |
| --- | --- |
| `auto` | Mine only while a recognised development session is active. Detected processes include Cursor, Claude Code, Windsurf, VS Code, JetBrains IDEs, and Aider. Idle outside those windows. |
| `always` | Mine continuously. Use this on dedicated machines. |
| `off` | Connect, authenticate, and subscribe to the pool, but do not hash. Useful for protocol-level testing. |

The detection list lives at `~/.vibenet/pairs.txt` (one process name per line) and is community-extensible by PR.

## Economics

A modern x86 CPU produces roughly 10–50 MH/s of SHA-256d. Bitcoin's network hashrate is approximately **700 EH/s** (7·10²⁰ H/s). A single CPU therefore represents on the order of **10⁻¹⁴** of the global hash share. The expected wait for a solo block on a single CPU exceeds the age of the universe by several orders of magnitude.

**VibeNet is not income-generating software.** Treat it as a lottery subscription whose ticket has a probability close to, but not exactly, zero. If you want measurable per-month CPU earnings, mine Monero (RandomX) instead — that is explicitly out of scope for this project.

The reason CPU solo BTC mining still exists as a category is that the *outcome* of a hit is asymmetric: a single share meeting the network target pays out the entire block reward rather than a proportional split. People accept the long odds for the asymmetric payoff. VibeNet is designed for that population.

## Architecture

```text
        Stratum v1 (JSON-RPC, newline-delimited, over TCP)
+--------------+ <---------------------------------> +-------------------+
|   stratum/   |                                     |  solo.ckpool.org  |
+------+-------+                                     +-------------------+
       │ jobs                                                ▲
       ▼                                                     │ shares
+--------------+
|    miner/    |   N × double-SHA256 worker goroutines
+--------------+
       ▲
       │ enabled / disabled
+--------------+
|     pair/    |   process enumeration → "vibe-paired" gating
+--------------+
```

- **`stratum/`** — Stratum v1 client. `mining.subscribe → mining.authorize → mining.notify → mining.submit`. Builds extranonce-1 / extranonce-2 / coinbase / merkle root / block header. Handles vardiff via `mining.set_difficulty`. Reconnect with exponential backoff.
- **`miner/`** — distributes a job across `N` workers, each iterating a disjoint extranonce-2 range. The hot path is a tight double-SHA256 loop on a stack-allocated 80-byte header; the per-share work is `≤ 5 µs` on commodity hardware.
- **`pair/`** — process enumeration with a small built-in allowlist (Cursor, Claude Code, Windsurf, VS Code, JetBrains, Aider). User-extensible at `~/.vibenet/pairs.txt`. Polled every 10 s.
- **`main.go`** — flag parsing, lifecycle, signal handling, status reporter.

## Protocol notes

The block header layout follows the canonical Bitcoin format:

```text
| version (4 LE) | prev_hash (32) | merkle_root (32) | ntime (4 LE) | nbits (4 LE) | nonce (4 LE) |
```

Stratum delivers `prev_hash` as eight 32-bit words in network byte order. Each word is byte-reversed before being placed in the header (this is the standard Stratum-v1 quirk and is the most common source of "all my shares are rejected" bugs in third-party clients).

The merkle root is computed locally:

```text
coinbase   = coinb1 ‖ extranonce1 ‖ extranonce2 ‖ coinb2
merkle     = SHA256d(coinbase)
for each branch in merkle_branch:
    merkle = SHA256d(merkle ‖ branch)
```

The pool's share target is derived from the advertised difficulty `d` as:

```text
target = ⌊ 0x00000000FFFF0000…00 / d ⌋        (256-bit big-endian)
```

A submission is accepted when `reverse(SHA256d(header)) ≤ target` interpreted as a 256-bit unsigned integer. The network target (used to detect an actual block) is encoded by `nbits` in the job; VibeNet evaluates both, but only the pool target gates `mining.submit`.

## Build from source

```sh
git clone https://github.com/erdinin/vibenet.git
cd vibenet
go build -o vibenet ./...
```

Requires Go ≥ 1.21. No CGO. No external dependencies — standard library only. Cross-compilation supported (`GOOS=linux GOARCH=amd64 go build …`).

## Roadmap

- [x] CPU SHA-256d hashing core (parallel workers, atomic counters)
- [x] Pool target derivation, hash-vs-target comparison
- [x] Live hashrate reporter (H/s · KH/s · MH/s · GH/s · TH/s)
- [x] Stratum v1 client (`subscribe / authorize / notify / submit / set_difficulty / set_extranonce`)
- [x] Job parsing and block header construction (prev-hash byte-swap, coinbase merkle root)
- [x] Job-driven mining loop with collision-free extranonce-2 sequence
- [x] End-to-end pool integration verified against `solo.ckpool.org`
- [x] Auto-pairing to active development sessions (`--mode auto`, default)
- [ ] Long-running share-acceptance verification at reduced pool difficulty
- [ ] Cross-platform idle-priority enforcement (`--intensity low`)
- [ ] Reconnect with exponential backoff
- [ ] Signed release binaries (Windows / Linux / macOS · amd64 + arm64)
- [ ] Optional Bitaxe USB ASIC support (Stratum passthrough)
- [ ] Optional local-node mode (skip ckpool, use your own `bitcoind` with `getblocktemplate`)
- [ ] Browser dashboard with pixel-art "mining office" (vibe-paired animation)

## Contributing

PRs welcome. The codebase is deliberately small and standard-library only — please keep it that way. Open an issue before any change larger than a focused patch.

When adding a process name to the pair list, attach a one-line justification ("editor", "AI coding tool", "terminal-based AI agent") in the PR description.

## Security & disclosure

VibeNet does not hold funds, does not implement a wallet, and has no upgrade mechanism. The only network endpoint it speaks to is the configured pool (default `solo.ckpool.org:3333`).

If you find a protocol-level bug that could cause shares to be rejected, leak the wallet address to an unintended destination, or cause the worker to hash against a stale or attacker-controlled job, please open a private security advisory on GitHub rather than a public issue.

## License

MIT. See [LICENSE](LICENSE).
