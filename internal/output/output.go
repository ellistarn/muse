package output

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// StageWidth is the fixed column width for stage names in log output.
const StageWidth = 13

// IsTTY reports whether stderr is connected to a terminal.
func IsTTY() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ── Stage logging ───────────────────────────────────────────────────────

// StageLog builds a consistent one-line log entry for a pipeline stage.
type StageLog struct {
	Name string
	Body string
	Tail string
}

// LogStage creates a stage log entry.
func LogStage(name, format string, args ...any) *StageLog {
	return &StageLog{Name: name, Body: fmt.Sprintf(format, args...)}
}

// SetTail sets the parenthetical suffix (duration, cost, etc.).
func (s *StageLog) SetTail(format string, args ...any) *StageLog {
	s.Tail = fmt.Sprintf(format, args...)
	return s
}

// Cost appends a duration+cost tail like "(3.1s, $0.0042)".
func (s *StageLog) Cost(d time.Duration, cost float64) *StageLog {
	if cost > 0 {
		s.Tail = fmt.Sprintf("(%s, $%.4f)", FormatDuration(d), cost)
	} else if d > time.Millisecond {
		s.Tail = fmt.Sprintf("(%s)", FormatDuration(d))
	}
	return s
}

// Duration appends a duration-only tail like "(3.1s)".
func (s *StageLog) Duration(d time.Duration) *StageLog {
	if d > time.Millisecond {
		s.Tail = fmt.Sprintf("(%s)", FormatDuration(d))
	}
	return s
}

// Print writes the formatted stage line to stderr.
func (s *StageLog) Print() {
	line := fmt.Sprintf("%-*s%s", StageWidth, s.Name, s.Body)
	if s.Tail != "" {
		line += " " + s.Tail
	}
	fmt.Fprintln(os.Stderr, line)
}

// LogBefore prints the "before" line for a slow stage and returns the
// stage name so the caller can print the aligned "→" result line after.
func LogBefore(name, format string, args ...any) string {
	fmt.Fprintf(os.Stderr, "%-*s%s\n", StageWidth, name, fmt.Sprintf(format, args...))
	return name
}

// LogAfter prints the aligned "→ result" line under a before-line.
func LogAfter(format string, args ...any) *StageLog {
	return &StageLog{Name: "", Body: fmt.Sprintf("→ %s", fmt.Sprintf(format, args...))}
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

// FormatSourceBreakdown formats a map of source→count as "(kiro: 12, opencode: 33)".
func FormatSourceBreakdown(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
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
	BarWidth = 20
	barFull  = "█"
	barEmpty = "░"
	tickRate = 200 * time.Millisecond
	ClearEsc = "\033[2K" // ANSI: erase entire line
)

// Progress renders an ephemeral progress bar on stderr that gets erased on stop.
type Progress struct {
	total   int
	counter *atomic.Int32
	done    chan struct{}
	exited  sync.WaitGroup
	tty     bool
}

// StartProgress begins rendering a progress bar. The bar is column-aligned
// under the stage name (indented by StageWidth). Call Stop() to erase it.
func StartProgress(total int, counter *atomic.Int32) *Progress {
	p := &Progress{
		total:   total,
		counter: counter,
		done:    make(chan struct{}),
		tty:     IsTTY(),
	}
	if p.tty && total > 0 {
		p.exited.Add(1)
		go p.run()
	}
	return p
}

func (p *Progress) run() {
	defer p.exited.Done()
	ticker := time.NewTicker(tickRate)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			n := int(p.counter.Load())
			bar := RenderBar(n, p.total, BarWidth)
			fmt.Fprintf(os.Stderr, "\r%-*s%s %d/%d", StageWidth, "", bar, n, p.total)
		}
	}
}

// Stop erases the progress bar line and returns.
func (p *Progress) Stop() {
	close(p.done)
	if p.tty && p.total > 0 {
		p.exited.Wait()
		fmt.Fprintf(os.Stderr, "\r%s\r", ClearEsc)
	}
}

// RenderBar draws a progress bar like [████████░░░░░░░░].
func RenderBar(n, total, width int) string {
	if total == 0 {
		return "[" + strings.Repeat(barEmpty, width) + "]"
	}
	filled := (n * width) / total
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat(barFull, filled) + strings.Repeat(barEmpty, width-filled) + "]"
}

// ClearLine erases the current terminal line.
func ClearLine() {
	fmt.Fprintf(os.Stderr, "\r%s\r", ClearEsc)
}
