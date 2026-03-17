package storage

import (
	"context"
	"fmt"
	"io"
)

// Sync copies data from src to dst. It is additive — items in dst that don't
// exist in src are left alone. If categories is empty, all categories are synced.
// Valid categories: "conversations", "observations", "muse".
func Sync(ctx context.Context, src, dst Store, categories []string, w io.Writer) error {
	all := len(categories) == 0
	cats := map[string]bool{}
	for _, c := range categories {
		cats[c] = true
	}

	if all || cats["conversations"] {
		n, err := syncConversations(ctx, src, dst)
		if err != nil {
			return fmt.Errorf("sync conversations: %w", err)
		}
		fmt.Fprintf(w, "Synced %d conversations\n", n)
	}

	if all || cats["observations"] {
		n, err := syncObservations(ctx, src, dst)
		if err != nil {
			return fmt.Errorf("sync observations: %w", err)
		}
		fmt.Fprintf(w, "Synced %d observations\n", n)
	}

	if all || cats["muse"] {
		n, err := syncMuse(ctx, src, dst)
		if err != nil {
			return fmt.Errorf("sync muse: %w", err)
		}
		fmt.Fprintf(w, "Synced %d muse versions\n", n)
	}

	return nil
}

func syncConversations(ctx context.Context, src, dst Store) (int, error) {
	entries, err := src.ListSessions(ctx)
	if err != nil {
		return 0, err
	}
	var count int
	for _, e := range entries {
		session, err := src.GetSession(ctx, e.Source, e.SessionID)
		if err != nil {
			return count, fmt.Errorf("get session %s: %w", e.Key, err)
		}
		if _, err := dst.PutSession(ctx, session); err != nil {
			return count, fmt.Errorf("put session %s: %w", e.Key, err)
		}
		count++
	}
	return count, nil
}

func syncObservations(ctx context.Context, src, dst Store) (int, error) {
	index, err := src.ListObservations(ctx)
	if err != nil {
		return 0, err
	}
	var count int
	for conversationKey := range index {
		content, err := src.GetObservation(ctx, conversationKey)
		if err != nil {
			return count, fmt.Errorf("get observation %s: %w", conversationKey, err)
		}
		if err := dst.PutObservation(ctx, conversationKey, content); err != nil {
			return count, fmt.Errorf("put observation %s: %w", conversationKey, err)
		}
		count++
	}
	return count, nil
}

func syncMuse(ctx context.Context, src, dst Store) (int, error) {
	timestamps, err := src.ListMuses(ctx)
	if err != nil {
		return 0, err
	}
	var count int
	for _, ts := range timestamps {
		content, err := src.GetMuseVersion(ctx, ts)
		if err != nil {
			return count, fmt.Errorf("get muse %s: %w", ts, err)
		}
		if err := dst.PutMuse(ctx, ts, content); err != nil {
			return count, fmt.Errorf("put muse %s: %w", ts, err)
		}
		count++
	}
	return count, nil
}
