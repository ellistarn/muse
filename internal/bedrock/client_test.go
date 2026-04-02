package bedrock

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/throttle"
)

type stubRuntime struct {
	out *bedrockruntime.ConverseOutput
	err error
}

func (s stubRuntime) Converse(_ context.Context, _ *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	return s.out, s.err
}

func TestConverseMessagesPreservesPartialResponseOnTruncation(t *testing.T) {
	client := NewClientWithRuntime(context.Background(), stubRuntime{
		out: &bedrockruntime.ConverseOutput{
			StopReason: types.StopReasonMaxTokens,
			Output: &types.ConverseOutputMemberMessage{
				Value: types.Message{
					Role: types.ConversationRoleAssistant,
					Content: []types.ContentBlock{
						&types.ContentBlockMemberText{Value: "part one "},
						&types.ContentBlockMemberText{Value: "part two"},
					},
				},
			},
			Usage: &types.TokenUsage{
				InputTokens:  aws.Int32(123),
				OutputTokens: aws.Int32(456),
			},
		},
	})

	resp, err := client.ConverseMessages(context.Background(), "system", []inference.Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected truncation error")
	}
	if resp == nil {
		t.Fatal("expected partial response")
	}
	if got, want := resp.Text, "part one part two"; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
	if got, want := resp.Usage.InputTokens, 123; got != want {
		t.Fatalf("InputTokens = %d, want %d", got, want)
	}
	if got, want := resp.Usage.OutputTokens, 456; got != want {
		t.Fatalf("OutputTokens = %d, want %d", got, want)
	}
	if !inference.IsTruncated(err) {
		t.Fatalf("err = %v, want TruncatedError", err)
	}
}

func TestIsThrottling(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"throttling exception", fmt.Errorf("ThrottlingException: rate exceeded"), true},
		{"too many tokens", fmt.Errorf("Too many tokens, please wait"), true},
		{"other error", fmt.Errorf("internal server error"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isThrottling(tt.err); got != tt.want {
				t.Errorf("isThrottling(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestRetryThrottled_Integration verifies that the retry+throttle plumbing
// works end-to-end with a live AIMD limiter. This replaces the old
// BatchTailRecovery test — the AIMD algorithm itself is tested in
// internal/throttle.
func TestRetryThrottled_Integration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := &Client{
		runtime: stubRuntime{},
		model:   "test-model",
		limiter: throttle.NewAIMDLimiter(ctx, throttle.Config{
			SeedRate: 100,
			MaxRate:  200,
		}),
	}

	const items = 30
	const concurrency = 10

	var completed atomic.Int32
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	start := time.Now()
	for range items {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			err := c.retryThrottled(ctx, func() error {
				return nil
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			completed.Add(1)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if got := completed.Load(); got != items {
		t.Fatalf("completed %d/%d items", got, items)
	}

	// At 100 req/s, 30 items should complete in well under 2 seconds.
	if elapsed > 2*time.Second {
		t.Fatalf("batch took %s — expected < 2s at 100 req/s", elapsed.Round(time.Millisecond))
	}
}
