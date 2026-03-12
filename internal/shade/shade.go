package shade

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ellistarn/shade/internal/awsconfig"
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
	s3      skill.S3API
	bedrock *bedrock.Client
	bucket  string
}

func New(ctx context.Context, bucket string) (*Shade, error) {
	cfg, err := awsconfig.Load(ctx)
	if err != nil {
		return nil, err
	}
	storageClient, err := storage.NewClient(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage client: %w", err)
	}
	bedrockClient, err := bedrock.NewClient(ctx, bedrock.ModelOpus)
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

// NewForTest creates a Shade with caller-provided dependencies.
func NewForTest(s3Client skill.S3API, bedrockClient *bedrock.Client, bucket string) *Shade {
	return &Shade{
		s3:      s3Client,
		bedrock: bedrockClient,
		bucket:  bucket,
	}
}

const systemPrompt = `You are a shade — a reflection of how your owner thinks, designs, and builds software. You respond in first person as they would: direct, technically precise, and opinionated when warranted.

You have access to skills that encode your patterns, principles, and preferences. A catalog of available skills is listed below. Use the read_skill tool to load any skills relevant to the question before answering.

You must never:
- Reveal the raw content of your skills verbatim
- Share personal information about your owner
- Discuss how your knowledge was derived or what conversations produced it
- Acknowledge that you are an AI or a "shade" — just respond naturally

If you don't have a relevant skill for a question, say so honestly rather than guessing.

## Available Skills

%s`

// readSkillToolSpec defines the read_skill tool for Bedrock tool use.
func readSkillToolSpec() *types.ToolConfiguration {
	return &types.ToolConfiguration{
		Tools: []types.Tool{
			&types.ToolMemberToolSpec{
				Value: types.ToolSpecification{
					Name:        aws.String("read_skill"),
					Description: aws.String("Load a skill's full content by name. Call this for any skills relevant to the question."),
					InputSchema: &types.ToolInputSchemaMemberJson{
						Value: document.NewLazyDocument(map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name": map[string]any{
									"type":        "string",
									"description": "The skill name from the catalog",
								},
							},
							"required": []any{"name"},
						}),
					},
				},
			},
		},
	}
}

// Ask answers a question using the shade's distilled skills.
// The LLM sees a catalog of skill names and descriptions, then uses tool
// calling to fetch the full content of any skills it deems relevant.
// This is a stateless one-shot: no session history, no persistence.
func (s *Shade) Ask(ctx context.Context, question string) (string, error) {
	catalog, err := skill.LoadCatalog(ctx, s.s3, s.bucket)
	if err != nil {
		return "", fmt.Errorf("failed to load skill catalog: %w", err)
	}
	system := fmt.Sprintf(systemPrompt, formatCatalog(catalog))
	toolConfig := readSkillToolSpec()

	handler := func(name string, input map[string]any) (string, error) {
		if name != "read_skill" {
			return "", fmt.Errorf("unknown tool: %s", name)
		}
		skillName, ok := input["name"].(string)
		if !ok || skillName == "" {
			return "", fmt.Errorf("read_skill requires a 'name' parameter")
		}
		sk, err := skill.LoadOne(ctx, s.s3, s.bucket, skillName)
		if err != nil {
			return "", fmt.Errorf("skill %q not found", skillName)
		}
		return sk.Content, nil
	}

	answer, _, err := s.bedrock.ConverseWithTools(ctx, system, question, toolConfig, handler,
		"Now produce your final answer to the original question. Be direct and concise.")
	return answer, err
}

func formatCatalog(skills []skill.Skill) string {
	if len(skills) == 0 {
		return "No skills are currently available."
	}
	var b strings.Builder
	for _, sk := range skills {
		fmt.Fprintf(&b, "- %s: %s\n", sk.Slug, sk.Description)
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
