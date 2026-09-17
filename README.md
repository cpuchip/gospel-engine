# gospel-engine

A project to contain all the tools needed to make the gospel library come alive with search and index capabilities, this depends on having the gospel library downloaded (see [gospel-library-downloader](https://github.com/cpuchip/gospel-library-downloader), a tool to download the Gospel Library from the Church of Jesus Christ of Latter-day Saints into Markdown).

The hosted server runs at **engine.ibeco.me**: a PostgreSQL + pgvector backend with keyword (FTS), semantic (vector), and hybrid search over scriptures, conference talks, manuals, books, and study aids.

## Using it as an MCP server

The same three tools — `gospel_search`, `gospel_get`, `gospel_list` — are available two ways:

### 1. Remote HTTP MCP (recommended — no local binary)

The server exposes a streamable-HTTP MCP endpoint at `/mcp`, exa-search / dnd-tools style. A bridge or client dials it directly:

```
https://engine.ibeco.me/mcp?key=<GOSPEL_MCP_KEY>
```

The key rides in the URL query. It is set on the server via the `GOSPEL_MCP_KEY` environment variable (see `.env.example`); an unset key fails closed (every `/mcp` request is rejected).

Example client config (Claude Code / VS Code style):

```json
{
  "gospel-engine": {
    "type": "http",
    "url": "https://engine.ibeco.me/mcp?key=YOUR_KEY_HERE"
  }
}
```

For a substrate bridge that resolves `$env:` placeholders, the URL can carry one (e.g. `https://engine.ibeco.me/mcp?key=$env:GOSPEL_MCP_KEY`).

### 2. Local stdio client (`gospel-mcp`)

The `gospel-mcp` binary is a thin stdio bridge that translates MCP JSON-RPC into the server's REST API (`/api/search`, `/api/get`, `/api/list`) using a `Bearer stdy_…` token (`GOSPEL_ENGINE_TOKEN`). It still works and is self-updating; the cross-compiled binaries are served from `/download/gospel-mcp-{os}-{arch}`. Prefer the remote HTTP MCP above unless you specifically need a local process.

## Search and semantic embeddings

Search runs entirely server-side, so semantic results are identical whether the caller used the HTTP MCP endpoint, the stdio client, or the REST API. Query-time embeddings use an OpenAI-compatible backend (`EMBEDDING_URL`, default `nomic-embed-text-v1.5`); when that backend is unreachable, keyword and hybrid search still work and semantic degrades gracefully (see `/api/health`).
