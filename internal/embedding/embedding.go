// Package embedding provides text-to-vector embedding via Bedrock Titan.
package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"github.com/ellistarn/muse/internal/awsconfig"
)

const (
	// ModelTitanV2 is the Bedrock model ID for Titan Embeddings V2.
	ModelTitanV2 = "amazon.titan-embed-text-v2:0"

	// requestsPerSec controls the rate limit for embedding calls.
	requestsPerSec = 10
)

// Embedder produces vector embeddings from text.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float64, error)
	Model() string
}

// titanRequest is the InvokeModel request body for Titan Embeddings V2.
type titanRequest struct {
	InputText string `json:"inputText"`
}

// titanResponse is the InvokeModel response body from Titan Embeddings V2.
type titanResponse struct {
	Embedding []float64 `json:"embedding"`
}

// Client calls Bedrock InvokeModel for Titan Embeddings V2.
type Client struct {
	runtime  *bedrockruntime.Client
	model    string
	throttle chan struct{}
}

// NewClient creates an embedding client with rate limiting.
func NewClient(ctx context.Context) (*Client, error) {
	cfg, err := awsconfig.Load(ctx)
	if err != nil {
		return nil, err
	}
	model := ModelTitanV2
	if override := os.Getenv("MUSE_EMBEDDING_MODEL"); override != "" {
		model = override
	}
	c := &Client{
		runtime:  bedrockruntime.NewFromConfig(cfg),
		model:    model,
		throttle: make(chan struct{}, requestsPerSec),
	}
	go c.refillTokens(ctx)
	return c, nil
}

// Model returns the model ID used for embeddings.
func (c *Client) Model() string {
	return c.model
}

// Embed produces embedding vectors for each input text. Calls are made in
// parallel with rate limiting. The returned slice is in the same order as input.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	results := make([][]float64, len(texts))
	errs := make([]error, len(texts))

	var wg sync.WaitGroup
	for i, text := range texts {
		wg.Add(1)
		go func(i int, text string) {
			defer wg.Done()
			vec, err := c.embedOne(ctx, text)
			results[i] = vec
			errs[i] = err
		}(i, text)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("embedding %d failed: %w", i, err)
		}
	}
	return results, nil
}

func (c *Client) embedOne(ctx context.Context, text string) ([]float64, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.throttle:
	}

	body, err := json.Marshal(titanRequest{InputText: text})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	out, err := c.runtime.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(c.model),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        body,
	})
	if err != nil {
		return nil, fmt.Errorf("invoke model: %w", err)
	}

	var resp titanResponse
	if err := json.Unmarshal(out.Body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return resp.Embedding, nil
}

func (c *Client) refillTokens(ctx context.Context) {
	ticker := time.NewTicker(time.Second / time.Duration(requestsPerSec))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			select {
			case c.throttle <- struct{}{}:
			default:
			}
		}
	}
}

// MockEmbedder is a test double that returns fixed-dimension vectors.
type MockEmbedder struct {
	Dimension int
	Vectors   map[string][]float64 // optional: specific text -> vector mapping
}

// NewMockEmbedder creates a MockEmbedder with the given dimension.
func NewMockEmbedder(dimension int) *MockEmbedder {
	return &MockEmbedder{Dimension: dimension, Vectors: map[string][]float64{}}
}

// Model returns a test model ID.
func (m *MockEmbedder) Model() string {
	return "mock-embedding-model"
}

// Embed returns mock vectors. If a specific mapping exists, uses that;
// otherwise generates a deterministic vector based on text hash.
func (m *MockEmbedder) Embed(_ context.Context, texts []string) ([][]float64, error) {
	results := make([][]float64, len(texts))
	for i, text := range texts {
		if vec, ok := m.Vectors[text]; ok {
			results[i] = vec
			continue
		}
		vec := make([]float64, m.Dimension)
		for j := range vec {
			// Deterministic based on text content and position
			h := 0
			for _, c := range text {
				h = h*31 + int(c)
			}
			vec[j] = float64((h+j*7)%1000) / 1000.0
		}
		results[i] = vec
	}
	return results, nil
}

// Compile-time interface check.
var _ Embedder = (*MockEmbedder)(nil)
