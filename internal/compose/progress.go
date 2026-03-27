package compose

import (
	"fmt"
	"math"
	"os"
	"sort"
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
		s.tail = fmt.Sprintf("(%s, $%.4f)", FormatDuration(d), u.Cost())
	} else if d > time.Millisecond {
		s.tail = fmt.Sprintf("(%s)", FormatDuration(d))
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

// FormatDuration formats a duration for display.
func FormatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// FormatBytes formats a byte count for display.
func FormatBytes(b int) string {
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
	// Sort for determinism
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
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
		tty:            isTTY(),
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

		s.textTokens += tokens
		if s.tty && s.textBudget > 0 {
			bar := renderBar(s.textTokens, s.textBudget, barWidth)
			fmt.Fprintf(os.Stderr, "\r%-*s%s writing...", stageWidth, "", bar)
		}
	}
}

// finish erases any active progress bar so the next log line starts clean.
func (s *stageStream) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tty && (s.thinkingBudget > 0 || s.textBudget > 0) {
		fmt.Fprintf(os.Stderr, "\r%s\r", clearLine)
	}
}
