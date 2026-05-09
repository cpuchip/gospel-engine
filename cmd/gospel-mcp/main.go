// Command gospel-mcp is the thin MCP client that bridges stdio JSON-RPC
// (used by VS Code Copilot, Claude Desktop, etc.) to the hosted
// gospel-engine HTTP API at study.ibeco.me.
//
// On startup the client checks the server's /api/version endpoint and,
// if the running version differs from the server's, downloads the new
// platform-specific binary, verifies its SHA-256, and atomically swaps.
// Self-update can be disabled with GOSPEL_AUTO_UPDATE=false.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cpuchip/gospel-engine/internal/selfupdate"
)

// Build-time version. The Dockerfile bakes this in via -ldflags.
var version = "dev"

func main() {
	endpoint := envOr("GOSPEL_ENGINE_URL", "https://study.ibeco.me")
	token := os.Getenv("GOSPEL_ENGINE_TOKEN")
	autoUpdate := envOr("GOSPEL_AUTO_UPDATE", "true") != "false"

	if autoUpdate {
		// Best-effort, runs in the foreground so a successful update can
		// re-exec before we touch stdio.
		if err := selfupdate.Check(endpoint, version); err != nil {
			log.Printf("self-update skipped: %v", err)
		}
	}

	cli := &client{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
	if err := cli.serve(); err != nil {
		log.Fatalf("mcp server error: %v", err)
	}
}

// ===== JSON-RPC types =====

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResp struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ===== client =====

type client struct {
	endpoint string
	token    string
	http     *http.Client
}

func (c *client) serve() error {
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)

	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var req rpcReq
		if err := json.Unmarshal(line, &req); err != nil {
			send(encoder, rpcResp{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		c.handle(encoder, &req)
	}
}

func (c *client) handle(enc *json.Encoder, req *rpcReq) {
	switch req.Method {
	case "initialize":
		send(enc, rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]string{"name": "gospel-engine", "version": version},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}})
	case "tools/list":
		send(enc, rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": tools}})
	case "tools/call":
		c.handleToolCall(enc, req)
	case "notifications/initialized":
		// no-op
	default:
		// JSON-RPC 2.0: notifications (no id) MUST NOT receive a
		// response, even an error. Only error on real requests.
		if req.ID != nil {
			send(enc, rpcResp{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found"}})
		}
	}
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (c *client) handleToolCall(enc *json.Encoder, req *rpcReq) {
	var p toolCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		send(enc, rpcResp{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params"}})
		return
	}
	text, err := c.dispatchTool(p.Name, p.Arguments)
	if err != nil {
		send(enc, rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"content": []contentItem{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}},
			"isError": true,
		}})
		return
	}
	send(enc, rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
		"content": []contentItem{{Type: "text", Text: text}},
	}})
}

func (c *client) dispatchTool(name string, args json.RawMessage) (string, error) {
	switch name {
	case "gospel_search":
		var a struct {
			Query   string   `json:"query"`
			Mode    string   `json:"mode"`
			Sources []string `json:"sources"`
			Limit   int      `json:"limit"`
		}
		_ = json.Unmarshal(args, &a)
		q := url.Values{}
		q.Set("q", a.Query)
		if a.Mode != "" {
			q.Set("mode", a.Mode)
		}
		if a.Limit > 0 {
			q.Set("limit", fmt.Sprint(a.Limit))
		}
		if len(a.Sources) > 0 {
			q.Set("sources", strings.Join(a.Sources, ","))
		}
		return c.callJSON("GET", "/api/search?"+q.Encode(), nil)

	case "gospel_get":
		var a struct {
			Reference string `json:"reference"`
			Type      string `json:"type"`
			ID        int64  `json:"id"`
		}
		_ = json.Unmarshal(args, &a)
		q := url.Values{}
		if a.Reference != "" {
			q.Set("ref", a.Reference)
		} else {
			q.Set("type", a.Type)
			q.Set("id", fmt.Sprint(a.ID))
		}
		return c.callJSON("GET", "/api/get?"+q.Encode(), nil)

	case "gospel_list":
		var a struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(args, &a)
		q := url.Values{}
		if a.Type != "" {
			q.Set("type", a.Type)
		}
		return c.callJSON("GET", "/api/list?"+q.Encode(), nil)

	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func (c *client) callJSON(method, path string, body any) (string, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.endpoint+path, rdr)
	if err != nil {
		return "", err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}
	// Pretty-print for the LLM.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err == nil {
		return pretty.String(), nil
	}
	return string(raw), nil
}

func send(enc *json.Encoder, resp rpcResp) {
	_ = enc.Encode(resp)
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// ===== tool definitions exposed to the MCP client =====

var tools = []map[string]any{
	{
		"name":        "gospel_search",
		"description": "Search scriptures, conference talks, manuals, and books. Modes: keyword (FTS), semantic (vector), hybrid (RRF merge — default).",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":   map[string]any{"type": "string", "description": "Natural-language search query"},
				"mode":    map[string]any{"type": "string", "enum": []string{"keyword", "semantic", "hybrid"}, "description": "Search mode (default: hybrid)"},
				"sources": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Subset of: scriptures, talks, manuals, books"},
				"limit":   map[string]any{"type": "integer", "description": "Max results (default 20, cap 100)"},
			},
			"required": []string{"query"},
		},
	},
	{
		"name":        "gospel_get",
		"description": "Retrieve a single record. Either pass `reference` (e.g. \"1 Nephi 3:7\") for scripture, or `type` + `id` for any content type.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reference": map[string]any{"type": "string"},
				"type":      map[string]any{"type": "string", "enum": []string{"scriptures", "talks", "manuals", "books"}},
				"id":        map[string]any{"type": "integer"},
			},
		},
	},
	{
		"name":        "gospel_list",
		"description": "List available content. type=scriptures returns volume summaries; type=talks lists conference sessions; type=manuals lists collections; type=books lists collections; omit type for overall stats.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"type": map[string]any{"type": "string", "enum": []string{"scriptures", "talks", "manuals", "books"}},
			},
		},
	},
}
