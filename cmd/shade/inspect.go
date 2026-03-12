package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ellistarn/shade/internal/bedrock"
	"github.com/ellistarn/shade/internal/log"
	"github.com/ellistarn/shade/internal/skill"
	"github.com/ellistarn/shade/internal/storage"
)

func newInspectCmd() *cobra.Command {
	var diff bool
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Inspect distilled skills",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireBucket(); err != nil {
				return err
			}
			ctx := cmd.Context()
			store, err := storage.NewClient(ctx, bucket)
			if err != nil {
				return err
			}
			s3Client := store.S3()

			if diff {
				return runDiff(cmd, store, s3Client)
			}

			log.Println("Loading skills...")
			skills, err := skill.LoadAll(ctx, s3Client, bucket)
			if err != nil {
				return fmt.Errorf("failed to load skills: %w", err)
			}
			if len(skills) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No skills found. Run 'shade dream' to generate skills from memories.")
				return nil
			}
			log.Printf("Found %d skills\n", len(skills))
			sort.Slice(skills, func(i, j int) bool { return skills[i].Slug < skills[j].Slug })
			for i, sk := range skills {
				if i > 0 {
					fmt.Fprintln(cmd.OutOrStdout())
				}
				fmt.Fprintf(cmd.OutOrStdout(), "=== %s ===\n", sk.Name)
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", sk.Description)
				fmt.Fprintln(cmd.OutOrStdout())
				fmt.Fprintln(cmd.OutOrStdout(), sk.Content)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&diff, "diff", false, "summarize what changed since the last dream")
	return cmd
}

func runDiff(cmd *cobra.Command, store *storage.Client, s3Client skill.S3API) error {
	ctx := cmd.Context()

	log.Println("Loading dream history...")
	dreams, err := store.ListDreams(ctx)
	if err != nil {
		return fmt.Errorf("failed to list dream history: %w", err)
	}
	if len(dreams) == 0 {
		return fmt.Errorf("no dream history found; run 'shade dream' to create a snapshot")
	}
	latest := dreams[len(dreams)-1]
	log.Printf("Comparing snapshot %s with current skills\n", latest)

	prev, err := store.GetDreamSkills(ctx, latest)
	if err != nil {
		return fmt.Errorf("failed to load dream snapshot %s: %w", latest, err)
	}
	current, err := skill.LoadAll(ctx, s3Client, bucket)
	if err != nil {
		return fmt.Errorf("failed to load current skills: %w", err)
	}
	log.Printf("Previous: %d skills, Current: %d skills\n", len(prev), len(current))

	if len(prev) == 0 && len(current) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No skills in either snapshot.")
		return nil
	}

	prevText := formatSkillSet("Previous skills", prev)
	currText := formatSkillSet("Current skills", current)

	log.Println("Generating diff summary...")
	llm, err := bedrock.NewClient(ctx, bedrock.ModelSonnet)
	if err != nil {
		return err
	}

	prompt := `Compare the previous and current skill sets. Summarize what changed in a few concise bullet points: which skills were added, removed, or meaningfully revised. Focus on substance, not formatting.`

	summary, usage, err := llm.Converse(ctx, prompt, prevText+"\n\n---\n\n"+currText)
	if err != nil {
		return fmt.Errorf("failed to generate diff summary: %w", err)
	}
	log.Printf("Diff complete ($%.4f)\n", usage.Cost())
	fmt.Fprintf(cmd.OutOrStdout(), "Changes since %s:\n\n%s\n", latest, strings.TrimSpace(summary))
	return nil
}

func formatSkillSet(label string, skills []skill.Skill) string {
	if len(skills) == 0 {
		return fmt.Sprintf("%s: (none)", label)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Slug < skills[j].Slug })
	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n\n", label)
	for _, sk := range skills {
		fmt.Fprintf(&b, "## %s\n%s\n\n%s\n\n", sk.Name, sk.Description, sk.Content)
	}
	return b.String()
}
