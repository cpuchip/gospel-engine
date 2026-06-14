// Package mcpserver exposes gospel-engine over MCP as a streamable-HTTP
// endpoint (mounted at /mcp), so a remote bridge can dial it like exa-search
// or dnd-tools — no local stdio binary required.
//
// The tools (gospel_search / gospel_get / gospel_list) are intentionally
// identical to the ones the stdio gospel-mcp client exposes. Rather than
// re-implement the search/get/list logic, each handler issues an in-process
// request against the server's own chi router (carrying a trusted context so
// the bearer-auth middleware is skipped — the /mcp endpoint has its own ?key=
// gate). This guarantees the MCP path and the REST path never diverge:
// semantic search, cross-references, reference parsing, and JSON shapes are all
// exactly what /api/search, /api/get, and /api/list return today.
package mcpserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/cpuchip/gospel-engine/internal/auth"
)

// Server wraps an mcp-go MCPServer whose tool handlers proxy in-process to the
// gospel-engine chi router.
type Server struct {
	mcp    *server.MCPServer
	router http.Handler
}

// New builds the MCP server. router is the gospel-engine chi router (from
// api.Server.Router()); the tool handlers call it in-process.
func New(router http.Handler, version string) *Server {
	m := server.NewMCPServer("gospel-engine", version, server.WithToolCapabilities(true))
	s := &Server{mcp: m, router: router}
	s.register()
	return s
}

// HTTPHandler returns the MCP server as a streamable-HTTP handler, mounted at
// /mcp so a remote bridge can dial it (exa-search / dnd-tools style).
func (s *Server) HTTPHandler() http.Handler {
	return server.NewStreamableHTTPServer(s.mcp, server.WithEndpointPath("/mcp"))
}

// callAPI performs an in-process GET against the chi router and returns the
// response body. The request carries a trusted context so the auth middleware
// passes without a bearer token (the /mcp endpoint is gated separately by
// ?key=). Non-2xx responses surface as an error with the body text.
func (s *Server) callAPI(ctx context.Context, path string) (string, error) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(auth.WithInternalTrusted(ctx))
	req.Header.Set("Accept", "application/json")

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Body)
	if rec.Code >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", rec.Code, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

func (s *Server) register() {
	// --- gospel_search -------------------------------------------------
	s.mcp.AddTool(
		mcp.NewTool("gospel_search",
			mcp.WithDescription("Search scriptures, conference talks, manuals, books, and study aids (Topical Guide, Bible Dictionary, Guide to the Scriptures, JST excerpts). Modes: keyword (FTS), semantic (vector), hybrid (RRF merge — default)."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Natural-language search query")),
			mcp.WithString("mode", mcp.Description("Search mode: keyword | semantic | hybrid (default: hybrid)"),
				mcp.Enum("keyword", "semantic", "hybrid")),
			mcp.WithArray("sources", mcp.Description("Subset of: scriptures, talks, manuals, books, study_aids"),
				mcp.Items(map[string]any{"type": "string", "enum": []string{"scriptures", "talks", "manuals", "books", "study_aids"}})),
			mcp.WithNumber("limit", mcp.Description("Max results (default 20, cap 100)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query := strings.TrimSpace(req.GetString("query", ""))
			if query == "" {
				return mcp.NewToolResultError("query is required"), nil
			}
			q := url.Values{}
			q.Set("q", query)
			if mode := req.GetString("mode", ""); mode != "" {
				q.Set("mode", mode)
			}
			if limit := req.GetInt("limit", 0); limit > 0 {
				q.Set("limit", fmt.Sprint(limit))
			}
			if sources := req.GetStringSlice("sources", nil); len(sources) > 0 {
				q.Set("sources", strings.Join(sources, ","))
			}
			body, err := s.callAPI(ctx, "/api/search?"+q.Encode())
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(body), nil
		},
	)

	// --- gospel_get ----------------------------------------------------
	s.mcp.AddTool(
		mcp.NewTool("gospel_get",
			mcp.WithDescription("Retrieve scripture by reference, or any other record by type+id.\n\n"+
				"`reference` accepts:\n"+
				"  • single verse — \"1 Nephi 3:7\", \"Matt 5:14\"\n"+
				"  • verse range  — \"D&C 93:24-30\" (capped at 50 verses)\n"+
				"  • whole chapter — \"Mosiah 4\" (returns chapters row with full_content)\n\n"+
				"Set `cross_refs: true` on a single-verse or verse-range query to also "+
				"receive footnote-derived cross-references. Off by default.\n\n"+
				"For talks/manuals/books/study_aids, omit `reference` and pass `type` + `id`."),
			mcp.WithString("reference", mcp.Description("Scripture reference: \"1 Nephi 3:7\", \"D&C 93:24-30\", \"Mosiah 4\".")),
			mcp.WithString("type", mcp.Description("Record type for id lookups"),
				mcp.Enum("scriptures", "talks", "manuals", "books", "study_aids")),
			mcp.WithNumber("id", mcp.Description("Record id (with type=)")),
			mcp.WithBoolean("cross_refs", mcp.Description("Include footnote-derived cross-references (opt-in; default false). Ignored for chapter and type+id lookups.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			q := url.Values{}
			if ref := strings.TrimSpace(req.GetString("reference", "")); ref != "" {
				q.Set("reference", ref)
			} else {
				typ := strings.TrimSpace(req.GetString("type", ""))
				id := req.GetInt("id", 0)
				if typ == "" || id == 0 {
					return mcp.NewToolResultError("provide either reference, or both type and id"), nil
				}
				q.Set("type", typ)
				q.Set("id", fmt.Sprint(id))
			}
			if req.GetBool("cross_refs", false) {
				q.Set("cross_refs", "true")
			}
			body, err := s.callAPI(ctx, "/api/get?"+q.Encode())
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(body), nil
		},
	)

	// --- gospel_list ---------------------------------------------------
	s.mcp.AddTool(
		mcp.NewTool("gospel_list",
			mcp.WithDescription("List available content. type=scriptures returns volume summaries; type=talks lists conference sessions; type=manuals lists collections; type=books lists collections; type=study_aids returns per-aid-type counts (tg/bd/gs/jst); omit type for overall stats."),
			mcp.WithString("type", mcp.Description("Content type to list (omit for overall stats)"),
				mcp.Enum("scriptures", "talks", "manuals", "books", "study_aids")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			q := url.Values{}
			if typ := strings.TrimSpace(req.GetString("type", "")); typ != "" {
				q.Set("type", typ)
			}
			body, err := s.callAPI(ctx, "/api/list?"+q.Encode())
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(body), nil
		},
	)
}
