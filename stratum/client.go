// Package stratum implements a Stratum v1 client suitable for connecting to
// a Bitcoin solo pool (e.g. solo.ckpool.org). It speaks JSON-RPC over a
// newline-delimited TCP connection and exposes the resulting job stream,
// difficulty updates, and share-submission RPC to higher layers.
//
// The package deliberately stops at the protocol boundary: it does not build
// block headers, evaluate targets, or own a mining loop. Header construction
// lives in stratum/job.go (next milestone); evaluation lives in target.go.
package stratum

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosed is returned by in-flight RPC calls when the underlying connection
// is terminated before a response arrives.
var ErrClosed = errors.New("stratum: connection closed")

// MiningNotify is a parsed mining.notify notification — a complete job
// template the miner must work on.
type MiningNotify struct {
	JobID        string
	PrevHashHex  string
	Coinb1Hex    string
	Coinb2Hex    string
	MerkleBranch []string // each entry is 32-byte hex
	VersionHex   string
	NBitsHex     string
	NTimeHex     string
	CleanJobs    bool
}

// Subscription holds the per-connection extranonce-1 prefix negotiated at
// subscribe time and the size (in bytes) of the extranonce-2 the client is
// responsible for picking for each share.
type Subscription struct {
	ExtraNonce1     []byte
	ExtraNonce2Size int
}

// Share is a candidate share the miner has produced and wishes to submit.
type Share struct {
	JobID       string
	ExtraNonce2 []byte
	NTime       uint32
	Nonce       uint32
}

// Client owns one TCP connection to a Stratum v1 endpoint.
type Client struct {
	addr  string
	user  string
	pass  string
	agent string

	conn net.Conn
	rd   *bufio.Reader

	writeMu sync.Mutex
	nextID  atomic.Uint64

	pendingMu sync.Mutex
	pending   map[uint64]chan rpcResponse

	subscription atomic.Pointer[Subscription]
	difficulty   atomic.Pointer[float64]

	notifyCh chan MiningNotify
	diffCh   chan float64

	closed atomic.Bool
}

type rpcRequest struct {
	ID     uint64        `json:"id"`
	Method string        `json:"method"`
	Params []interface{} `json:"params"`
}

type rpcMessage struct {
	ID     *uint64         `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type rpcResponse struct {
	Result json.RawMessage
	Error  json.RawMessage
}

const (
	dialTimeout     = 10 * time.Second
	readDeadline    = 90 * time.Second // longer than typical pool keepalive
	writeDeadline   = 10 * time.Second
	notifyChanCap   = 4
	readBufferBytes = 32 * 1024 // generous: mining.notify with deep merkle branch can approach 2 KB
)

// Dial opens a TCP connection to addr and returns a Client. Call Run to start
// the subscribe/authorize handshake and the read loop.
func Dial(ctx context.Context, addr, user, pass, agent string) (*Client, error) {
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("stratum: dial %s: %w", addr, err)
	}
	return &Client{
		addr:     addr,
		user:     user,
		pass:     pass,
		agent:    agent,
		conn:     conn,
		rd:       bufio.NewReaderSize(conn, readBufferBytes),
		pending:  make(map[uint64]chan rpcResponse),
		notifyCh: make(chan MiningNotify, notifyChanCap),
		diffCh:   make(chan float64, notifyChanCap),
	}, nil
}

// Run performs subscribe + authorize, then blocks reading and dispatching
// messages until ctx is done or the connection drops.
func (c *Client) Run(ctx context.Context) error {
	readDone := make(chan error, 1)
	go func() { readDone <- c.readLoop() }()

	sub, err := c.subscribe(ctx)
	if err != nil {
		c.Close()
		<-readDone
		return fmt.Errorf("stratum: subscribe: %w", err)
	}
	c.subscription.Store(sub)

	if err := c.authorize(ctx, c.user, c.pass); err != nil {
		c.Close()
		<-readDone
		return fmt.Errorf("stratum: authorize: %w", err)
	}

	select {
	case <-ctx.Done():
		c.Close()
		<-readDone
		return ctx.Err()
	case err := <-readDone:
		c.Close()
		return err
	}
}

func (c *Client) readLoop() error {
	for {
		if c.closed.Load() {
			return nil
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(readDeadline))
		line, err := c.rd.ReadBytes('\n')
		if err != nil {
			if c.closed.Load() {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		if len(line) == 0 {
			continue
		}
		if err := c.dispatch(line); err != nil {
			return err
		}
	}
}

func (c *Client) dispatch(raw []byte) error {
	var m rpcMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("parse json: %w", err)
	}

	// Response: has id, no method.
	if m.ID != nil && m.Method == "" {
		c.pendingMu.Lock()
		ch, ok := c.pending[*m.ID]
		if ok {
			delete(c.pending, *m.ID)
		}
		c.pendingMu.Unlock()
		if ok {
			ch <- rpcResponse{Result: m.Result, Error: m.Error}
		}
		return nil
	}

	switch m.Method {
	case "mining.notify":
		return c.handleNotify(m.Params)
	case "mining.set_difficulty":
		return c.handleSetDifficulty(m.Params)
	case "mining.set_extranonce":
		return c.handleSetExtraNonce(m.Params)
	case "client.reconnect":
		return errors.New("server requested reconnect")
	default:
		// Unknown methods are ignored; do not break the connection over them.
		return nil
	}
}

func (c *Client) handleNotify(params json.RawMessage) error {
	var p []json.RawMessage
	if err := json.Unmarshal(params, &p); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	if len(p) < 9 {
		return fmt.Errorf("notify: expected 9 params, got %d", len(p))
	}
	var n MiningNotify
	for idx, dst := range []interface{}{
		&n.JobID, &n.PrevHashHex, &n.Coinb1Hex, &n.Coinb2Hex,
		&n.MerkleBranch, &n.VersionHex, &n.NBitsHex, &n.NTimeHex, &n.CleanJobs,
	} {
		if err := json.Unmarshal(p[idx], dst); err != nil {
			return fmt.Errorf("notify param %d: %w", idx, err)
		}
	}
	deliverDropOldest(c.notifyCh, n)
	return nil
}

func (c *Client) handleSetDifficulty(params json.RawMessage) error {
	var p []float64
	if err := json.Unmarshal(params, &p); err != nil {
		return fmt.Errorf("set_difficulty: %w", err)
	}
	if len(p) < 1 {
		return errors.New("set_difficulty: empty params")
	}
	d := p[0]
	c.difficulty.Store(&d)
	deliverDropOldest(c.diffCh, d)
	return nil
}

func (c *Client) handleSetExtraNonce(params json.RawMessage) error {
	var p []json.RawMessage
	if err := json.Unmarshal(params, &p); err != nil {
		return fmt.Errorf("set_extranonce: %w", err)
	}
	if len(p) < 2 {
		return errors.New("set_extranonce: expected 2 params")
	}
	var enHex string
	if err := json.Unmarshal(p[0], &enHex); err != nil {
		return err
	}
	en, err := hex.DecodeString(enHex)
	if err != nil {
		return fmt.Errorf("set_extranonce: hex: %w", err)
	}
	var size int
	if err := json.Unmarshal(p[1], &size); err != nil {
		return err
	}
	c.subscription.Store(&Subscription{ExtraNonce1: en, ExtraNonce2Size: size})
	return nil
}

func (c *Client) subscribe(ctx context.Context) (*Subscription, error) {
	resp, err := c.call(ctx, "mining.subscribe", []interface{}{c.agent})
	if err != nil {
		return nil, err
	}
	if err := protocolError(resp.Error); err != nil {
		return nil, err
	}
	// Result layout: [ [[method, sub_id], ...], extranonce1_hex, extranonce2_size ]
	var arr []json.RawMessage
	if err := json.Unmarshal(resp.Result, &arr); err != nil {
		return nil, fmt.Errorf("result: %w", err)
	}
	if len(arr) < 3 {
		return nil, fmt.Errorf("result: expected 3 entries, got %d", len(arr))
	}
	var enHex string
	if err := json.Unmarshal(arr[1], &enHex); err != nil {
		return nil, fmt.Errorf("extranonce1: %w", err)
	}
	en, err := hex.DecodeString(enHex)
	if err != nil {
		return nil, fmt.Errorf("extranonce1 hex: %w", err)
	}
	var size int
	if err := json.Unmarshal(arr[2], &size); err != nil {
		return nil, fmt.Errorf("extranonce2_size: %w", err)
	}
	return &Subscription{ExtraNonce1: en, ExtraNonce2Size: size}, nil
}

func (c *Client) authorize(ctx context.Context, user, pass string) error {
	resp, err := c.call(ctx, "mining.authorize", []interface{}{user, pass})
	if err != nil {
		return err
	}
	if err := protocolError(resp.Error); err != nil {
		return err
	}
	var ok bool
	if err := json.Unmarshal(resp.Result, &ok); err != nil {
		return fmt.Errorf("result: %w", err)
	}
	if !ok {
		return errors.New("pool rejected credentials")
	}
	return nil
}

// Submit sends mining.submit and waits for the response. The boolean reflects
// the pool's accept/reject verdict; reason carries the pool-provided error
// payload when non-empty.
func (c *Client) Submit(ctx context.Context, worker string, s Share) (accepted bool, reason string, err error) {
	resp, err := c.call(ctx, "mining.submit", []interface{}{
		worker,
		s.JobID,
		hex.EncodeToString(s.ExtraNonce2),
		hex.EncodeToString(binary.BigEndian.AppendUint32(nil, s.NTime)),
		hex.EncodeToString(binary.BigEndian.AppendUint32(nil, s.Nonce)),
	})
	if err != nil {
		return false, "", err
	}
	if perr := protocolError(resp.Error); perr != nil {
		return false, perr.Error(), nil
	}
	var ok bool
	if err := json.Unmarshal(resp.Result, &ok); err != nil {
		return false, "", fmt.Errorf("submit result: %w", err)
	}
	return ok, "", nil
}

func (c *Client) call(ctx context.Context, method string, params []interface{}) (rpcResponse, error) {
	id := c.nextID.Add(1)
	ch := make(chan rpcResponse, 1)

	c.pendingMu.Lock()
	if c.closed.Load() {
		c.pendingMu.Unlock()
		return rpcResponse{}, ErrClosed
	}
	c.pending[id] = ch
	c.pendingMu.Unlock()

	if err := c.send(rpcRequest{ID: id, Method: method, Params: params}); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return rpcResponse{}, fmt.Errorf("send %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return rpcResponse{}, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return rpcResponse{}, ErrClosed
		}
		return resp, nil
	}
}

func (c *Client) send(req rpcRequest) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	_, err = c.conn.Write(b)
	return err
}

// Notifications returns the channel that delivers job notifications.
// The channel buffer holds the most recent notifyChanCap notifications;
// when full, the oldest is dropped to make room.
func (c *Client) Notifications() <-chan MiningNotify { return c.notifyCh }

// Difficulty returns the channel that delivers difficulty updates with
// the same drop-oldest discipline as Notifications.
func (c *Client) Difficulty() <-chan float64 { return c.diffCh }

// Subscription returns the most recent extranonce-1 / extranonce-2 size from
// the pool. nil before subscribe completes.
func (c *Client) Subscription() *Subscription { return c.subscription.Load() }

// CurrentDifficulty returns the most recently received pool difficulty, or
// 0 before set_difficulty arrives.
func (c *Client) CurrentDifficulty() float64 {
	if p := c.difficulty.Load(); p != nil {
		return *p
	}
	return 0
}

// Close terminates the underlying connection and fails any in-flight RPC
// calls with ErrClosed. Safe to call multiple times.
func (c *Client) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	err := c.conn.Close()
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
	return err
}

// protocolError converts a JSON-RPC error payload into a Go error. Returns nil
// when the payload is absent or the JSON literal null.
func protocolError(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return fmt.Errorf("server error: %s", raw)
}

// deliverDropOldest sends v on ch, evicting the oldest buffered value when ch
// is full. Used for state-style notifications where the latest reading
// supersedes earlier ones (jobs after a clean_jobs=true, difficulty changes).
func deliverDropOldest[T any](ch chan T, v T) {
	for {
		select {
		case ch <- v:
			return
		default:
			select {
			case <-ch:
			default:
			}
		}
	}
}
