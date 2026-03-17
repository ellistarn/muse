// Package runlog provides append-only logging of API usage records.
// Each muse command that calls an LLM writes a single JSONL record,
// enabling cost tracking via "muse usage".
package runlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Record captures a single command invocation's API usage.
type Record struct {
	Timestamp    time.Time `json:"ts"`
	Command      string    `json:"command"`
	InputHash    string    `json:"input_hash,omitempty"`
	Cached       bool      `json:"cached,omitempty"`
	InputTokens  int       `json:"in_tokens"`
	OutputTokens int       `json:"out_tokens"`
	Cost         float64   `json:"cost"`
	Discovered   int       `json:"discovered,omitempty"`
	Observed     int       `json:"observed,omitempty"`
	Pruned       int       `json:"pruned,omitempty"`
}

// Logger appends records to a JSONL file. A nil Logger is safe to use (no-op).
type Logger struct {
	path string
}

// New returns a Logger that writes to path.
func New(path string) *Logger {
	return &Logger{path: path}
}

// Log appends a record. Nil receiver is a no-op.
func (l *Logger) Log(r Record) error {
	if l == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("runlog: create dir: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("runlog: open: %w", err)
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(r)
}

// Read scans a JSONL file and returns records with timestamps after since.
// A zero since returns all records. Missing file returns nil, nil.
func Read(path string, since time.Time) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("runlog: open: %w", err)
	}
	defer f.Close()

	var records []Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var r Record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			continue // skip malformed lines
		}
		if !since.IsZero() && !r.Timestamp.After(since) {
			continue
		}
		records = append(records, r)
	}
	return records, scanner.Err()
}

// CommandSummary aggregates usage across runs of the same command.
type CommandSummary struct {
	Command      string
	Runs         int
	InputTokens  int
	OutputTokens int
	Cost         float64
}

// Summary groups records by command and returns summaries sorted by cost descending.
func Summary(records []Record) []CommandSummary {
	m := map[string]*CommandSummary{}
	for _, r := range records {
		s, ok := m[r.Command]
		if !ok {
			s = &CommandSummary{Command: r.Command}
			m[r.Command] = s
		}
		s.Runs++
		s.InputTokens += r.InputTokens
		s.OutputTokens += r.OutputTokens
		s.Cost += r.Cost
	}
	summaries := make([]CommandSummary, 0, len(m))
	for _, s := range m {
		summaries = append(summaries, *s)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Cost > summaries[j].Cost
	})
	return summaries
}
