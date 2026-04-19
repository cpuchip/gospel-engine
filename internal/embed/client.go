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

// Embed returns a single embedding for the given text.
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
