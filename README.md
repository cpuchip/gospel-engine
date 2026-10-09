# gospel-engine

A project to contain all the tools needed to make the gospel library come alive with search and index capabilities, this depends on having the gospel library downloaded (see [gospel-library-downloader](https://github.com/cpuchip/gospel-library-downloader), a tool to download the Gospel Library from the Church of Jesus Christ of Latter-day Saints into Markdown).

The hosted server runs at **engine.ibeco.me**: a PostgreSQL + pgvector backend with keyword (FTS), semantic (vector), and hybrid search over scriptures, conference talks, manuals, books, and study aids.

## Using it as an MCP server

The same five tools — `gospel_search`, `gospel_get`, `gospel_related`, `gospel_citations`, `gospel_list` — are available two ways:

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

The `gospel-mcp` binary is a thin stdio bridge that translates MCP JSON-RPC into the server's REST API (`/api/search`, `/api/get`, `/api/related`, `/api/citations`, `/api/list`) using a `Bearer stdy_…` token (`GOSPEL_ENGINE_TOKEN`). It still works and is self-updating; the cross-compiled binaries are served from `/download/gospel-mcp-{os}-{arch}`. Prefer the remote HTTP MCP above unless you specifically need a local process.

## Related passages (`gospel_related`)

`/api/related` walks the links the library itself makes, with no model involved: chapter footnotes (the same rows as `gospel_get ... cross_refs`), the verse lists of Topical Guide, Bible Dictionary and Guide to the Scriptures entries, and the scripture citations in each conference-talk and manual paragraph. Seed it with a `reference` (verse, range or chapter) or with `type` (`talks`, `manuals`, `study_aids`) and `id`; `hops` is 1 or 2, `kinds` narrows to `verses`, `talks`, `manuals`, `aids`. Results are ranked by hops, then by how many links reach them, and each carries `via` (the first link's type and direction, and on two hops the passage in between). The edges live in `graph_edges`, rebuilt after every index pass that changed content (`POST /api/admin/rebuild-graph` rebuilds on demand).

## Citations (`gospel_citations`)

`/api/citations?reference=Ether 12:27` (a verse or a verse range) answers who has cited it, live, from the [BYU Scripture Citation Index](https://scriptures.byu.edu/). The data is BYU's; every response says so (`source`, `source_url`) and every citation links back to it in the index (`index_url`), and to the talk at churchofjesuschrist.org when the index gives one (`talk_url`). The engine is a light client: one upstream request per uncached lookup, a User-Agent naming the engine and a contact address (`CITATION_CONTACT`), answers kept for a day (at most 5,000), no crawling or pre-fetching, and the per-token rate limit in front.

## Rate limits

Each `stdy_` token carries a rate limit (`api_tokens.rate_limit`, default 60 requests a minute), enforced per token across the REST API and `/mcp` together: a bucket of that many requests that refills at that rate. An empty bucket answers `429 Too Many Requests` with `Retry-After` in seconds. Admin tokens (minted only inside the container; ibeco.me's service token is one, and it carries every ibeco.me reader's lookups) get at least 6,000 a minute; the legacy shared `GOSPEL_MCP_KEY` is not limited. Buckets live in memory, so a restart refills them.

## Search and semantic embeddings

Search runs entirely server-side, so semantic results are identical whether the caller used the HTTP MCP endpoint, the stdio client, or the REST API. Query-time embeddings use an OpenAI-compatible backend (`EMBEDDING_URL`, default `nomic-embed-text-v1.5`); when that backend is unreachable, keyword and hybrid search still work and semantic degrades gracefully (see `/api/health`).
