package compose

import (
	"context"
	"sort"
	"testing"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/storage"
)

func TestResolveSources_Explicit(t *testing.T) {
	store := storage.NewLocalStoreWithRoot(t.TempDir())
	ctx := context.Background()

	// Explicit sources are returned as-is, regardless of what's on disk
	got, err := ResolveSources(ctx, store, []string{"github", "slack"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "github" || got[1] != "slack" {
		t.Errorf("expected [github slack], got %v", got)
	}
}

func TestResolveSources_SteadyState(t *testing.T) {
	store := storage.NewLocalStoreWithRoot(t.TempDir())
	ctx := context.Background()

	// Create observation directories for specific sources
	obs := &Observations{Items: []Observation{{Text: "test"}}}
	if err := PutObservations(ctx, store, "opencode", "conv1", obs); err != nil {
		t.Fatal(err)
	}
	if err := PutObservations(ctx, store, "slack", "conv2", obs); err != nil {
		t.Fatal(err)
	}

	// No explicit sources — should return sources that have observation dirs
	got, err := ResolveSources(ctx, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != "opencode" || got[1] != "slack" {
		t.Errorf("expected [opencode slack], got %v", got)
	}
}

func TestResolveSources_Bootstrap(t *testing.T) {
	store := storage.NewLocalStoreWithRoot(t.TempDir())
	ctx := context.Background()

	// No observation directories at all — bootstrap with defaults
	got, err := ResolveSources(ctx, store, nil)
	if err != nil {
		t.Fatal(err)
	}

	defaults := conversation.DefaultSourceNames()
	sort.Strings(defaults)
	sort.Strings(got)
	if len(got) != len(defaults) {
		t.Errorf("expected %d default sources, got %d: %v", len(defaults), len(got), got)
	}
	for i := range defaults {
		if got[i] != defaults[i] {
			t.Errorf("expected %s at position %d, got %s", defaults[i], i, got[i])
		}
	}

	// Verify .active sentinel files were created
	for _, src := range defaults {
		sources, err := ListObservationSources(ctx, store)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, s := range sources {
			if s == src {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected bootstrap to create observation dir for %s", src)
		}
	}
}

func TestResolveSources_RemovedSource(t *testing.T) {
	store := storage.NewLocalStoreWithRoot(t.TempDir())
	ctx := context.Background()

	// Set up two sources with observations
	obs := &Observations{Items: []Observation{{Text: "test"}}}
	if err := PutObservations(ctx, store, "opencode", "conv1", obs); err != nil {
		t.Fatal(err)
	}
	if err := PutObservations(ctx, store, "github", "conv2", obs); err != nil {
		t.Fatal(err)
	}

	// Remove github by deleting its observations
	if err := DeleteObservationsForSource(ctx, store, "github"); err != nil {
		t.Fatal(err)
	}

	// Should only return opencode now
	got, err := ResolveSources(ctx, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "opencode" {
		t.Errorf("expected [opencode], got %v", got)
	}
}

func TestEnsureSourceDir_Idempotent(t *testing.T) {
	store := storage.NewLocalStoreWithRoot(t.TempDir())
	ctx := context.Background()

	// Ensure creates the sentinel
	if err := EnsureSourceDir(ctx, store, "github"); err != nil {
		t.Fatal(err)
	}
	sources, err := ListObservationSources(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0] != "github" {
		t.Errorf("expected [github], got %v", sources)
	}

	// Calling again is a no-op (doesn't error or duplicate)
	if err := EnsureSourceDir(ctx, store, "github"); err != nil {
		t.Fatal(err)
	}

	// With real observations present, doesn't write sentinel
	obs := &Observations{Items: []Observation{{Text: "test"}}}
	if err := PutObservations(ctx, store, "github", "conv1", obs); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSourceDir(ctx, store, "github"); err != nil {
		t.Fatal(err)
	}
}
