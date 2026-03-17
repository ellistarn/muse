package runlog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLogAndRead_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	logger := New(path)

	now := time.Now().UTC().Truncate(time.Millisecond)
	r := Record{
		Timestamp:    now,
		Command:      "distill",
		InputHash:    "abc123",
		InputTokens:  5000,
		OutputTokens: 800,
		Cost:         0.63,
		Discovered:   5,
		Observed:     5,
		Pruned:       42,
	}
	if err := logger.Log(r); err != nil {
		t.Fatalf("Log: %v", err)
	}

	records, err := Read(path, time.Time{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	got := records[0]
	if got.Command != "distill" {
		t.Errorf("Command = %q, want %q", got.Command, "distill")
	}
	if got.InputHash != "abc123" {
		t.Errorf("InputHash = %q, want %q", got.InputHash, "abc123")
	}
	if got.Cost != 0.63 {
		t.Errorf("Cost = %f, want %f", got.Cost, 0.63)
	}
	if got.Discovered != 5 {
		t.Errorf("Discovered = %d, want 5", got.Discovered)
	}
}

func TestRead_TimeFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	logger := New(path)

	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	logger.Log(Record{Timestamp: old, Command: "distill", Cost: 1.00})
	logger.Log(Record{Timestamp: recent, Command: "ask", Cost: 0.05})

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	records, err := Read(path, since)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record after filter, got %d", len(records))
	}
	if records[0].Command != "ask" {
		t.Errorf("expected 'ask', got %q", records[0].Command)
	}
}

func TestRead_MissingFile(t *testing.T) {
	dir := t.TempDir()
	records, err := Read(filepath.Join(dir, "nonexistent.jsonl"), time.Time{})
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected 0 records, got %d", len(records))
	}
}

func TestSummary_Aggregation(t *testing.T) {
	records := []Record{
		{Command: "distill", InputTokens: 50000, OutputTokens: 5000, Cost: 1.00},
		{Command: "distill", InputTokens: 60000, OutputTokens: 4000, Cost: 1.20},
		{Command: "ask", InputTokens: 5000, OutputTokens: 800, Cost: 0.04},
	}
	summaries := Summary(records)
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}
	// First should be distill (higher cost)
	if summaries[0].Command != "distill" {
		t.Errorf("expected distill first, got %q", summaries[0].Command)
	}
	if summaries[0].Runs != 2 {
		t.Errorf("distill runs = %d, want 2", summaries[0].Runs)
	}
	if summaries[0].InputTokens != 110000 {
		t.Errorf("distill InputTokens = %d, want 110000", summaries[0].InputTokens)
	}
	if summaries[1].Command != "ask" {
		t.Errorf("expected ask second, got %q", summaries[1].Command)
	}
}

func TestNilLogger_NoOp(t *testing.T) {
	var l *Logger
	if err := l.Log(Record{Command: "test"}); err != nil {
		t.Fatalf("nil logger should be no-op, got: %v", err)
	}
}

func TestLogAndRead_CachedField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	logger := New(path)

	logger.Log(Record{Timestamp: time.Now().UTC(), Command: "distill", Cached: true, Cost: 0})
	records, err := Read(path, time.Time{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !records[0].Cached {
		t.Error("expected Cached=true")
	}
}

func TestLog_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "runs.jsonl")
	logger := New(path)

	if err := logger.Log(Record{Command: "test"}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
}
