package distill

import (
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellistarn/muse/internal/inference"
)

// stageWidth is the fixed column width for stage names in log output.
const stageWidth = 13

// isTTY reports whether stderr is connected to a terminal.
func isTTY() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ── Stage logging ───────────────────────────────────────────────────────

// stageLog builds a consistent one-line log entry for a pipeline stage.
type stageLog struct {
	name string
	body string
	tail string
}

func logStage(name, format string, args ...any) *stageLog {
	return &stageLog{name: name, body: fmt.Sprintf(format, args...)}
}

func (s *stageLog) cost(d time.Duration, u inference.Usage) *stageLog {
	if u.Cost() > 0 {
		s.tail = fmt.Sprintf("(%s, $%.4f)", formatStageDuration(d), u.Cost())
	} else if d > time.Millisecond {
		s.tail = fmt.Sprintf("(%s)", formatStageDuration(d))
	}
	return s
}

func (s *stageLog) print() {
	line := fmt.Sprintf("%-*s%s", stageWidth, s.name, s.body)
	if s.tail != "" {
		line += " " + s.tail
	}
	fmt.Fprintln(os.Stderr, line)
}

// logBefore prints the "before" line for a slow stage and returns the
// stage name so the caller can print the aligned "→" result line after.
func logBefore(name, format string, args ...any) string {
	fmt.Fprintf(os.Stderr, "%-*s%s\n", stageWidth, name, fmt.Sprintf(format, args...))
	return name
}

// logAfter prints the aligned "→ result" line under a before-line.
func logAfter(format string, args ...any) *stageLog {
	return &stageLog{name: "", body: fmt.Sprintf("→ %s", fmt.Sprintf(format, args...))}
}

func formatStageDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func formatBytes(b int) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// formatSourceBreakdown formats a map of source→count as "(kiro: 12, opencode: 33)".
func formatSourceBreakdown(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	// Sort for determinism — use sorted keys
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	// Sort alphabetically
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s: %d", k, counts[k])
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// ── Progress bar ────────────────────────────────────────────────────────

const (
	barWidth  = 20
	barFull   = "█"
	barEmpty  = "░"
	tickRate  = 200 * time.Millisecond
	clearLine = "\033[2K" // ANSI: erase entire line
)

// progress renders an ephemeral progress bar on stderr that gets erased on stop.
type progress struct {
	total   int
	counter *atomic.Int32
	done    chan struct{}
	exited  sync.WaitGroup
	tty     bool
}

// startProgress begins rendering a progress bar. The bar is column-aligned
// under the stage name (indented by stageWidth). Call stop() to erase it.
func startProgress(total int, counter *atomic.Int32) *progress {
	p := &progress{
		total:   total,
		counter: counter,
		done:    make(chan struct{}),
		tty:     isTTY(),
	}
	if p.tty && total > 0 {
		p.exited.Add(1)
		go p.run()
	}
	return p
}

func (p *progress) run() {
	defer p.exited.Done()
	ticker := time.NewTicker(tickRate)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			n := int(p.counter.Load())
			bar := renderBar(n, p.total, barWidth)
			fmt.Fprintf(os.Stderr, "\r%-*s%s %d/%d", stageWidth, "", bar, n, p.total)
		}
	}
}

// stop erases the progress bar line and returns.
func (p *progress) stop() {
	close(p.done)
	if p.tty && p.total > 0 {
		p.exited.Wait()
		fmt.Fprintf(os.Stderr, "\r%s\r", clearLine)
	}
}

// renderBar draws a progress bar like [████████░░░░░░░░].
func renderBar(n, total, width int) string {
	if total == 0 {
		return "[" + strings.Repeat(barEmpty, width) + "]"
	}
	filled := (n * width) / total
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat(barFull, filled) + strings.Repeat(barEmpty, width-filled) + "]"
}

// ── Stage stream ────────────────────────────────────────────────────────

// stageStream renders streaming LLM output for a single-call stage like merge
// or diff. During thinking, it shows a progress bar against the token budget.
// Once text tokens arrive, it streams the actual output to stderr.
type stageStream struct {
	thinkingBudget int  // 0 means no thinking bar (just stream text)
	thinkingTokens int  // accumulated thinking tokens
	textStarted    bool // flipped on first non-thinking delta
	wroteOutput    bool // true if any text was written to stderr
	tty            bool // whether stderr is a terminal
	mu             sync.Mutex
}

// newStageStream creates a stream renderer. thinkingBudget of 0 disables the
// thinking progress bar (deltas stream directly).
func newStageStream(thinkingBudget int) *stageStream {
	return &stageStream{
		thinkingBudget: thinkingBudget,
		tty:            isTTY(),
	}
}

// callback returns an inference.StreamFunc suitable for ConverseStream.
func (s *stageStream) callback() inference.StreamFunc {
	return func(delta inference.StreamDelta) {
		s.mu.Lock()
		defer s.mu.Unlock()

		if delta.Thinking {
			s.thinkingTokens += int(math.Ceil(float64(len(delta.Text)) / 4.0))
			if s.tty && s.thinkingBudget > 0 {
				bar := renderBar(s.thinkingTokens, s.thinkingBudget, barWidth)
				fmt.Fprintf(os.Stderr, "\r%-*s%s thinking...", stageWidth, "", bar)
			}
			return
		}

		// First text token — erase the thinking bar
		if !s.textStarted {
			s.textStarted = true
			if s.tty && s.thinkingBudget > 0 {
				fmt.Fprintf(os.Stderr, "\r%s\r", clearLine)
			}
		}

		fmt.Fprint(os.Stderr, delta.Text)
		s.wroteOutput = true
	}
}

// finish prints a trailing newline if any text was streamed, ensuring the
// subsequent logAfter line starts on a fresh line.
func (s *stageStream) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wroteOutput {
		fmt.Fprintln(os.Stderr)
	} else if s.tty && s.thinkingBudget > 0 {
		// No text was streamed but we showed a thinking bar — erase it
		fmt.Fprintf(os.Stderr, "\r%s\r", clearLine)
	}
}
