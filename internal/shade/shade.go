package shade

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ellistarn/shade/internal/bedrock"
	"github.com/ellistarn/shade/internal/skill"
	"github.com/ellistarn/shade/internal/source"
	"github.com/ellistarn/shade/internal/storage"
)

// UploadResult summarizes what happened during an upload sync.
type UploadResult struct {
	Total    int
	Uploaded int
	Skipped  int
	Warnings []string
}

// Shade holds the state needed for all operations.
type Shade struct {
	storage *storage.Client
	s3      *s3.Client
	bedrock *bedrock.Client
	bucket  string
}

func New(ctx context.Context, bucket string) (*Shade, error) {
	storageClient, err := storage.NewClient(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage client: %w", err)
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-west-2"))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	bedrockClient, err := bedrock.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create bedrock client: %w", err)
	}
	return &Shade{
		storage: storageClient,
		s3:      s3.NewFromConfig(cfg),
		bedrock: bedrockClient,
		bucket:  bucket,
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
	skills, err := skill.LoadAll(ctx, s.s3, s.bucket)
	if err != nil {
		return "", fmt.Errorf("failed to load skills: %w", err)
	}
	system := fmt.Sprintf(systemPrompt, formatSkills(skills))
	return s.bedrock.Converse(ctx, system, question)
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
	existing, err := s.storage.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list remote sessions: %w", err)
	}
	remote := map[string]storage.SessionEntry{}
	for _, e := range existing {
		remote[e.Key] = e
	}

	var local []source.Session
	var warnings []string
	if sessions, err := source.OpenCodeSessions(); err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to read OpenCode sessions: %v", err))
	} else {
		local = append(local, sessions...)
	}
	if sessions, err := source.ClaudeCodeSessions(); err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to read Claude Code sessions: %v", err))
	} else {
		local = append(local, sessions...)
	}

	var uploaded, skipped int
	for i := range local {
		sess := &local[i]
		key := fmt.Sprintf("memories/%s/%s.json", sess.Source, sess.SessionID)
		if entry, exists := remote[key]; exists {
			if !sess.UpdatedAt.After(entry.LastModified) {
				skipped++
				continue
			}
		}
		if err := s.storage.PutSession(ctx, sess); err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to upload %s: %v", sess.SessionID, err))
			continue
		}
		uploaded++
	}
	return &UploadResult{
		Total:    len(local),
		Uploaded: uploaded,
		Skipped:  skipped,
		Warnings: warnings,
	}, nil
}

// Ls returns all session entries from S3.
func (s *Shade) Ls(ctx context.Context) ([]storage.SessionEntry, error) {
	entries, err := s.storage.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	return entries, nil
}

// Show retrieves a specific session from S3. If source is empty, both sources
// are tried.
func (s *Shade) Show(ctx context.Context, sessionID string, src string) (*source.Session, error) {
	sources := []string{src}
	if src == "" {
		sources = []string{"opencode", "claude-code"}
	}
	for _, source := range sources {
		session, err := s.storage.GetSession(ctx, source, sessionID)
		if err != nil {
			continue
		}
		return session, nil
	}
	return nil, fmt.Errorf("session %s not found", sessionID)
}
