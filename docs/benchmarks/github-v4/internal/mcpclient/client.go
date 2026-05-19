// Package mcpclient is a minimal JSON-RPC-over-stdio MCP client.
//
// Just enough surface for the bench: initialize, tools/list,
// tools/call. mcp-go ships a server library but no client; rather
// than depend on the broader ecosystem we hand-roll the three
// methods the runner needs.
//
// Wire format: newline-delimited JSON-RPC 2.0. The MCP spec defines
// optional notifications, capabilities exchange, etc — the runner
// doesn't care about any of that, so neither does this client.
package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Client talks to one MCP server over a pair of io.ReadWriter
// streams. Concurrent calls are NOT supported — the bench runs one
// tool call at a time per trial, so a mutex around requests is
// enough.
type Client struct {
	in     *bufio.Scanner
	out    io.Writer
	nextID atomic.Int64
	mu     sync.Mutex
}

// New builds a Client over the given read + write streams. Wrap a
// child process's stdout (Read) and stdin (Write) to talk to it.
func New(read io.Reader, write io.Writer) *Client {
	s := bufio.NewScanner(read)
	// MCP messages can be large (tools/list with verbose tool
	// descriptions, big tool results). Default 64KB scanner buf
	// truncates them — bump to 8MB.
	s.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return &Client{in: s, out: write}
}

// Initialize performs the handshake. Returns when the server has
// acknowledged the protocol version, or err if the response carries
// one. Pass clientName + clientVersion for the server-side log.
func (c *Client) Initialize(ctx context.Context, clientName, clientVersion string) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    clientName,
			"version": clientVersion,
		},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	// Some servers require the initialized notification. Send it.
	if err := c.notify("notifications/initialized", nil); err != nil {
		return fmt.Errorf("send initialized notification: %w", err)
	}
	return nil
}

// Tool is the subset of an MCP tool definition the bench needs:
// name, description, JSON-schema input. Maps directly onto
// Anthropic's ToolParam.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolsList enumerates the server's registered tools.
func (c *Client) ToolsList(ctx context.Context) ([]Tool, error) {
	resp, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	var out struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	return out.Tools, nil
}

// ToolsCall dispatches a single tool invocation. arguments is the
// tool's input JSON object (map[string]any is fine). Returns the
// content-block array as raw JSON so the caller can decide how to
// project it — typically just stringify the text blocks.
func (c *Client) ToolsCall(ctx context.Context, name string, arguments any) (string, error) {
	resp, err := c.call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		return "", fmt.Errorf("tools/call %s: %w", name, err)
	}
	// Result shape: { content: [{type: "text", text: "..."}], isError: bool }
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return "", fmt.Errorf("decode tools/call result: %w", err)
	}
	var text string
	for _, b := range out.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	if out.IsError {
		// Return text but signal error via empty-string + err path
		// is awkward — let the caller see the body and use the
		// isError flag itself if it cares. For the bench we treat
		// it like any other tool response.
		return text, nil
	}
	return text, nil
}

// call issues a request with a fresh ID and waits for the matching
// response. Notifications (no id) interleaved on the wire are
// skipped; we just keep reading until we see our id.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.nextID.Add(1)
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := c.out.Write(append(body, '\n')); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	// Read responses until we see our id or the context expires.
	// Server may emit notifications (no id) we ignore.
	type wireResp struct {
		ID     *int64          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	deadline := time.Now().Add(60 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("response timeout for id=%d", id)
		}
		if !c.in.Scan() {
			if err := c.in.Err(); err != nil {
				return nil, fmt.Errorf("read: %w", err)
			}
			return nil, fmt.Errorf("server closed before response id=%d", id)
		}
		line := c.in.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp wireResp
		if err := json.Unmarshal(line, &resp); err != nil {
			// Garbage line — log + skip rather than fail; the
			// server may have printed something to stdout we
			// don't care about.
			continue
		}
		if resp.ID == nil || *resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("server error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// notify sends a fire-and-forget JSON-RPC notification (no id, no
// response).
func (c *Client) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = c.out.Write(append(body, '\n'))
	return err
}
