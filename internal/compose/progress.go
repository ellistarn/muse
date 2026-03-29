package compose

import (
	"fmt"
	"math"
	"os"
	"sync"
	"sync/atomic"

	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/output"
)

// ── Stage logging (thin wrappers around output package) ─────────────────

// stageLog wraps output.StageLog with an inference.Usage-aware cost method.
type stageLog = output.StageLog

func logStage(name, format string, args ...any) *stageLog {
	return output.LogStage(name, format, args...)
}

func logBefore(name, format string, args ...any) string {
	return output.LogBefore(name, format, args...)
}

func logAfter(format string, args ...any) *stageLog {
	return output.LogAfter(format, args...)
}

// Re-export formatting functions used by cmd/compose.go.
var (
	FormatDuration        = output.FormatDuration
	FormatBytes           = output.FormatBytes
	formatSourceBreakdown = output.FormatSourceBreakdown
)

// ── Progress bar (delegate to output package) ───────────────────────────

func startProgress(total int, counter *atomic.Int32) *output.Progress {
	return output.StartProgress(total, counter)
}

// ── Stage stream (compose-specific, depends on inference.StreamFunc) ────

// stageStream renders streaming LLM output for a single-call stage like compose
// or diff. Shows a two-phase progress bar: thinking tokens against a thinking
// budget, then writing tokens against a text budget. No raw text is streamed.
type stageStream struct {
	thinkingBudget int // 0 means no thinking bar
	textBudget     int // 0 means no writing bar
	thinkingTokens int // accumulated thinking tokens
	textTokens     int // accumulated writing tokens
	textStarted    bool
	tty            bool
	mu             sync.Mutex
}

// newStageStream creates a two-phase progress renderer.
//   - thinkingBudget: expected thinking tokens (0 disables thinking bar)
//   - textBudget: expected output tokens (0 disables writing bar)
func newStageStream(thinkingBudget, textBudget int) *stageStream {
	return &stageStream{
		thinkingBudget: thinkingBudget,
		textBudget:     textBudget,
		tty:            output.IsTTY(),
	}
}

// callback returns an inference.StreamFunc suitable for ConverseStream.
func (s *stageStream) callback() inference.StreamFunc {
	return func(delta inference.StreamDelta) {
		s.mu.Lock()
		defer s.mu.Unlock()

		tokens := int(math.Ceil(float64(len(delta.Text)) / 4.0))

		if delta.Thinking {
			s.thinkingTokens += tokens
			if s.tty && s.thinkingBudget > 0 {
				bar := output.RenderBar(s.thinkingTokens, s.thinkingBudget, output.BarWidth)
				fmt.Fprintf(os.Stderr, "\r%-*s%s thinking...", output.StageWidth, "", bar)
			}
			return
		}

		// First text token — erase the thinking bar
		if !s.textStarted {
			s.textStarted = true
			if s.tty && s.thinkingBudget > 0 {
				output.ClearLine()
			}
		}

		s.textTokens += tokens
		if s.tty && s.textBudget > 0 {
			bar := output.RenderBar(s.textTokens, s.textBudget, output.BarWidth)
			fmt.Fprintf(os.Stderr, "\r%-*s%s writing...", output.StageWidth, "", bar)
		}
	}
}

// finish erases any active progress bar so the next log line starts clean.
func (s *stageStream) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tty && (s.thinkingBudget > 0 || s.textBudget > 0) {
		output.ClearLine()
	}
}
