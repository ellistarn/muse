package compose

import (
	"context"
	"fmt"
	"sort"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/storage"
)

// ResolveSources determines which sources to include in a compose run.
//
// The observation directory is the config — its presence means the source is
// active. The rules are:
//
//  1. Explicit sources passed (e.g. "muse compose github")? Use those exactly.
//  2. No explicit sources, observation directories exist? Use the set of sources
//     that have observation directories.
//  3. No explicit sources, no observation directories (first run)? Bootstrap
//     with all non-opt-in (default) sources.
//
// On bootstrap, observation directories are created for all default sources so
// that future runs remember them even if a source had no conversations on the
// first run.
func ResolveSources(ctx context.Context, store storage.Store, explicit []string) ([]string, error) {
	// Explicit sources: use them as-is
	if len(explicit) > 0 {
		return explicit, nil
	}

	// Check which sources have observation directories
	existing, err := ListObservationSources(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("list observation sources: %w", err)
	}

	// Steady state: observation directories exist, use them
	if len(existing) > 0 {
		sort.Strings(existing)
		return existing, nil
	}

	// Bootstrap: no observation directories yet, use defaults and create dirs
	defaults := conversation.DefaultSourceNames()
	for _, src := range defaults {
		if err := EnsureSourceDir(ctx, store, src); err != nil {
			return nil, fmt.Errorf("bootstrap source %s: %w", src, err)
		}
	}
	return defaults, nil
}

// EnsureSourceDir creates an observation directory for a source, marking it as
// active for future compose runs. This is a no-op if the directory already
// exists (i.e. has at least one observation).
func EnsureSourceDir(ctx context.Context, store storage.Store, source string) error {
	// Write a sentinel file so the directory exists even with no observations.
	// ListObservationSources looks for any files under observations/{source}/.
	key := fmt.Sprintf("observations/%s/.active", source)
	// Check if source already has observations — skip if so
	existing, err := store.ListData(ctx, fmt.Sprintf("observations/%s/", source))
	if err == nil && len(existing) > 0 {
		return nil
	}
	return store.PutData(ctx, key, []byte(""))
}

// RemoveSource deletes the observation directory for a source, deactivating it
// from future compose runs.
func RemoveSource(ctx context.Context, store storage.Store, source string) error {
	return DeleteObservationsForSource(ctx, store, source)
}

// ListObservationSources returns the unique source names that have observation
// directories (i.e. at least one file under observations/{source}/).
func ListObservationSources(ctx context.Context, store storage.Store) ([]string, error) {
	keys, err := store.ListData(ctx, "observations/")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, key := range keys {
		// keys look like "observations/{source}/{id}.json" or "observations/{source}/.active"
		rel := key[len("observations/"):]
		if idx := indexByte(rel, '/'); idx > 0 {
			seen[rel[:idx]] = true
		}
	}
	var sources []string
	for s := range seen {
		sources = append(sources, s)
	}
	return sources, nil
}

func indexByte(s string, c byte) int {
	for i := range s {
		if s[i] == c {
			return i
		}
	}
	return -1
}
