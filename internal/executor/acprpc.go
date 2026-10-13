package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// This file is the JSON-RPC 2.0 layer of the Agent Client Protocol client
// (https://agentclientprotocol.com): newline-delimited JSON messages over the
// agent's stdin/stdout, in both directions. The harness is the ACP *client*;
// the agent sends it requests too (fs/*, session/request_permission), so
// this is a peer, not a plain request/response caller.

// JSON-RPC error codes the client answers with.
const (
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

// acpMaxMessageBytes bounds one JSON-RPC line from the agent. A read_text_file
// reply is the big one on the agent->client side and goes the other way, so
// this is generous for anything an agent legitimately sends while still
// refusing an unbounded line from a hostile one.
const acpMaxMessageBytes = 32 << 20

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// rpcHandler answers one request the agent made. A non-nil *rpcError is sent
// back as the JSON-RPC error; otherwise result is marshalled.
type rpcHandler func(method string, params json.RawMessage) (result any, rerr *rpcError)

// acpConn is one JSON-RPC session with an agent process.
type acpConn struct {
	w       io.Writer
	wmu     sync.Mutex
	nextID  int64
	pmu     sync.Mutex
	pending map[string]chan rpcMessage
	closed  chan struct{}
	// closeErr is why the read loop stopped; read after closed is closed.
	closeErr error

	onRequest      rpcHandler
	onNotification func(method string, params json.RawMessage)
	// onNoise receives stdout lines that are not JSON-RPC. An agent that logs
	// to stdout is broken, but its log is exactly what explains why, so it
	// goes to the output tail rather than being dropped.
	onNoise func(line string)
}

func newACPConn(w io.Writer, onRequest rpcHandler, onNotification func(string, json.RawMessage), onNoise func(string)) *acpConn {
	return &acpConn{
		w:              w,
		pending:        map[string]chan rpcMessage{},
		closed:         make(chan struct{}),
		onRequest:      onRequest,
		onNotification: onNotification,
		onNoise:        onNoise,
	}
}

// readLoop reads messages until r ends. Requests from the agent are answered
// inline: every handler this client has is a local file operation or an
// immediate permission answer, so none blocks on the connection itself.
func (c *acpConn) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), acpMaxMessageBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m rpcMessage
		if err := json.Unmarshal(line, &m); err != nil || m.JSONRPC != "2.0" {
			if c.onNoise != nil {
				c.onNoise(string(line))
			}
			continue
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			c.answer(m)
		case m.Method != "":
			if c.onNotification != nil {
				c.onNotification(m.Method, m.Params)
			}
		case len(m.ID) > 0:
			c.pmu.Lock()
			ch, ok := c.pending[string(m.ID)]
			delete(c.pending, string(m.ID))
			c.pmu.Unlock()
			if ok {
				ch <- m
			}
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	c.closeErr = err
	close(c.closed)
}

func (c *acpConn) answer(m rpcMessage) {
	var result any
	var rerr *rpcError
	func() {
		// A handler bug must not take the whole session down with an
		// unanswered request the agent then waits on forever.
		defer func() {
			if p := recover(); p != nil {
				rerr = &rpcError{Code: rpcInternalError, Message: fmt.Sprintf("client error: %v", p)}
			}
		}()
		if c.onRequest == nil {
			rerr = &rpcError{Code: rpcMethodNotFound, Message: "method not found: " + m.Method}
			return
		}
		result, rerr = c.onRequest(m.Method, m.Params)
	}()
	out := rpcMessage{JSONRPC: "2.0", ID: m.ID}
	if rerr != nil {
		out.Error = rerr
	} else {
		b, err := json.Marshal(result)
		if err != nil {
			out.Error = &rpcError{Code: rpcInternalError, Message: err.Error()}
		} else {
			out.Result = b
		}
	}
	_ = c.send(out)
}

func (c *acpConn) send(m rpcMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(b)
	return err
}

// errACPClosed is returned for a call the agent never answered because its
// stdout ended -- it exited, or was killed.
var errACPClosed = errors.New("the agent closed its output")

// call sends a request and waits for its response, ctx, or the connection
// closing.
func (c *acpConn) call(ctx context.Context, method string, params, out any) error {
	c.pmu.Lock()
	c.nextID++
	id := json.RawMessage(strconv.FormatInt(c.nextID, 10))
	ch := make(chan rpcMessage, 1)
	c.pending[string(id)] = ch
	c.pmu.Unlock()
	forget := func() {
		c.pmu.Lock()
		delete(c.pending, string(id))
		c.pmu.Unlock()
	}

	p, err := json.Marshal(params)
	if err != nil {
		forget()
		return err
	}
	if err := c.send(rpcMessage{JSONRPC: "2.0", ID: id, Method: method, Params: p}); err != nil {
		forget()
		return fmt.Errorf("acp: send %s: %w", method, err)
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return fmt.Errorf("acp: %s: %w", method, m.Error)
		}
		if out != nil && len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, out); err != nil {
				return fmt.Errorf("acp: %s: decode result: %w", method, err)
			}
		}
		return nil
	case <-c.closed:
		forget()
		return fmt.Errorf("acp: %s: %w", method, errACPClosed)
	case <-ctx.Done():
		forget()
		return ctx.Err()
	}
}

// notify sends a notification (no response expected).
func (c *acpConn) notify(method string, params any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.send(rpcMessage{JSONRPC: "2.0", Method: method, Params: p})
}
