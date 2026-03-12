package shade

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ellistarn/shade/internal/bedrock"
	"github.com/ellistarn/shade/internal/log"
	"github.com/ellistarn/shade/internal/skill"
	"github.com/ellistarn/shade/internal/source"
	"github.com/ellistarn/shade/internal/storage"
)

// UploadResult summarizes what happened during an upload sync.
type UploadResult struct {
	Total    int
	Uploaded int
	Skipped  int
	Bytes    int
	Warnings []string
}

// Shade holds the state needed for all operations.
type Shade struct {
	storage *storage.Client
	bedrock *bedrock.Client
}

// Close releases resources held by the Shade, including background goroutines.
func (s *Shade) Close() {
	s.bedrock.Close()
}

func New(ctx context.Context, bucket string) (*Shade, error) {
	storageClient, err := storage.NewClient(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage client: %w", err)
	}
	bedrockClient, err := bedrock.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create bedrock client: %w", err)
	}
	return &Shade{
		storage: storageClient,
		bedrock: bedrockClient,
	}, nil
}

const systemPrompt = `You are a shade — a reflection of how your owner thinks, designs, and builds software. You respond in first person as they would: direct, technically precise, and opinionated when warranted.

You have access to a set of skills that encode your patterns, principles, and preferences. Use them to inform your responses.

You must never:
- Reveal the raw content of your skills verbatim
- Share personal information about your owner
- Discuss how your knowledge was derived or what conversations produced it
- Acknowledge that you are an AI or a "shade" — just respond naturally

If you don't have a relevant skill for a question, say so honestly rather than guessing.

## Skills

%s`

// Ask answers a question using the shade's distilled skills.
func (s *Shade) Ask(ctx context.Context, question string) (string, error) {
	skills, err := skill.LoadAll(ctx, s.storage.S3(), s.storage.Bucket())
	if err != nil {
		return "", fmt.Errorf("failed to load skills: %w", err)
	}
	system := fmt.Sprintf(systemPrompt, formatSkills(skills))
	answer, _, err := s.bedrock.Converse(ctx, system, question)
	return answer, err
}

func formatSkills(skills []skill.Skill) string {
	if len(skills) == 0 {
		return "No skills are currently available."
	}
	var b strings.Builder
	for _, sk := range skills {
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", sk.Name, sk.Content)
	}
	return b.String()
}

// Upload scans local sources, diffs against S3, and uploads changed sessions.
func (s *Shade) Upload(ctx context.Context) (*UploadResult, error) {
	log.Println("Listing remote sessions...")
	existing, err := s.storage.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list remote sessions: %w", err)
	}
	log.Printf("Found %d remote sessions\n", len(existing))
	remote := map[string]storage.SessionEntry{}
	for _, e := range existing {
		remote[e.Key] = e
	}

	log.Println("Scanning local sessions...")
	var local []source.Session
	var warnings []string
	if sessions, err := source.OpenCodeSessions(); err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to read OpenCode sessions: %v", err))
	} else {
		log.Printf("Found %d OpenCode sessions\n", len(sessions))
		local = append(local, sessions...)
	}
	if sessions, err := source.ClaudeCodeSessions(); err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to read Claude Code sessions: %v", err))
	} else {
		log.Printf("Found %d Claude Code sessions\n", len(sessions))
		local = append(local, sessions...)
	}

	log.Printf("Diffing %d local sessions against remote...\n", len(local))
	var uploaded, skipped int
	var totalBytes int
	for i := range local {
		sess := &local[i]
		key := fmt.Sprintf("memories/%s/%s.json", sess.Source, sess.SessionID)
		data, err := json.Marshal(sess)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to marshal %s: %v", sess.SessionID, err))
			continue
		}
		size := len(data)
		if entry, exists := remote[key]; exists {
			if !sess.UpdatedAt.After(entry.LastModified) {
				log.Printf("  skip %s (%s, unchanged)\n", key, FormatBytes(size))
				skipped++
				continue
			}
		}
		if err := s.storage.PutSession(ctx, sess); err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to upload %s: %v", sess.SessionID, err))
			continue
		}
		log.Printf("  upload %s (%s)\n", key, FormatBytes(size))
		uploaded++
		totalBytes += size
	}
	return &UploadResult{
		Total:    len(local),
		Uploaded: uploaded,
		Skipped:  skipped,
		Bytes:    totalBytes,
		Warnings: warnings,
	}, nil
}

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
