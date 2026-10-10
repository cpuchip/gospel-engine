// Package embed is a thin OpenAI-compatible embeddings client (LM Studio,
// llama.cpp server, vLLM, etc. all speak this).
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client embeds text using a fixed model. The model identifier is part of
// the Client because in this system it MUST match the model that produced
// the bulk-loaded embeddings — mixing models silently breaks similarity.
type Client struct {
	BaseURL string
	Model   string
	HTTP    *http.Client

	// QueryPrefix and DocumentPrefix are the task instructions the model's
	// card asks for (nomic-embed-text-v1.5: "search_query: " and
	// "search_document: "). Empty sends text as-is, the engine's behaviour
	// before v3. Set both from a preset with SetTaskPrefixes, never one alone:
	// queries and the corpus must be embedded the same way.
	QueryPrefix    string
	DocumentPrefix string
}

// taskPrefixes are the known presets for EMBED_TASK_PREFIXES. A named preset,
// not raw strings in the environment: deploy tools trim trailing spaces, and
// "search_query:" without its space is a different input to the model.
var taskPrefixes = map[string][2]string{
	"":           {"", ""},
	"none":       {"", ""},
	"nomic-v1.5": {"search_query: ", "search_document: "},
}

// SetTaskPrefixes applies a preset by name; an unknown name is an error so a
// typo cannot silently embed queries in a different space from the corpus.
func (c *Client) SetTaskPrefixes(name string) error {
	p, ok := taskPrefixes[name]
	if !ok {
		return fmt.Errorf("unknown EMBED_TASK_PREFIXES %q (known: none, nomic-v1.5)", name)
	}
	c.QueryPrefix, c.DocumentPrefix = p[0], p[1]
	return nil
}

// EmbedQuery embeds a search query with the query prefix.
func (c *Client) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	return c.Embed(ctx, c.QueryPrefix+q)
}

// EmbedDocument embeds corpus text (a verse, a paragraph) with the document prefix.
func (c *Client) EmbedDocument(ctx context.Context, text string) ([]float32, error) {
	return c.Embed(ctx, c.DocumentPrefix+text)
}

// New constructs a Client with a sensible default timeout.
func New(baseURL, model string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{
		BaseURL: baseURL,
		Model:   model,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

// Embed returns a single embedding for the given text, exactly as given (no
// prefix). Search and indexing call EmbedQuery and EmbedDocument.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(map[string]any{
		"model": c.Model,
		"input": text,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed API %d: %s", resp.StatusCode, string(raw))
	}

	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding embed response: %w", err)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("embedding response had no data")
	}
	return out.Data[0].Embedding, nil
}

// Ping checks the embedding server is reachable. Non-fatal if it fails —
// the server can still serve keyword search without embeddings.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Embed(ctx, "test")
	return err
}
