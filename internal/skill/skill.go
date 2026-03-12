package skill

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gopkg.in/yaml.v3"
)

// S3API is the subset of the S3 SDK used for skill loading.
// This is the mock boundary for tests.
type S3API interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// Skill represents a parsed SKILL.md file following the Agent Skills spec.
type Skill struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Slug        string `yaml:"-"` // directory name, used as the key for LoadOne
	Content     string `yaml:"-"` // full markdown body after frontmatter
}

// LoadAll fetches all skills from S3 under the skills/ prefix.
// Each skill lives at skills/{name}/SKILL.md.
func LoadAll(ctx context.Context, client S3API, bucket string) ([]Skill, error) {
	paths, err := listSkillPaths(ctx, client, bucket)
	if err != nil {
		return nil, err
	}
	var skills []Skill
	for _, path := range paths {
		sk, err := loadSkill(ctx, client, bucket, path)
		if err != nil {
			continue // skip unparseable skills
		}
		sk.Slug = slugFromKey(path)
		skills = append(skills, *sk)
	}
	return skills, nil
}

// LoadCatalog returns all skills with only Name, Description, and Slug populated.
// Content is left empty. This is the "menu" for progressive disclosure.
func LoadCatalog(ctx context.Context, client S3API, bucket string) ([]Skill, error) {
	paths, err := listSkillPaths(ctx, client, bucket)
	if err != nil {
		return nil, err
	}
	var catalog []Skill
	for _, path := range paths {
		sk, err := loadSkillMeta(ctx, client, bucket, path)
		if err != nil {
			continue // skip unparseable skills
		}
		sk.Slug = slugFromKey(path)
		catalog = append(catalog, *sk)
	}
	return catalog, nil
}

// LoadOne fetches a single skill by name from S3.
func LoadOne(ctx context.Context, client S3API, bucket, name string) (*Skill, error) {
	key := fmt.Sprintf("skills/%s/SKILL.md", name)
	return loadSkill(ctx, client, bucket, key)
}

func listSkillPaths(ctx context.Context, client S3API, bucket string) ([]string, error) {
	prefix := "skills/"
	var paths []string
	// Manual pagination using the S3API interface
	var continuationToken *string
	for {
		out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            &bucket,
			Prefix:            aws.String(prefix),
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list skills: %w", err)
		}
		for _, obj := range out.Contents {
			key := aws.ToString(obj.Key)
			if strings.HasSuffix(key, "/SKILL.md") {
				paths = append(paths, key)
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		continuationToken = out.NextContinuationToken
	}
	return paths, nil
}

// loadSkillMeta fetches a skill file and parses only the YAML frontmatter.
// The markdown body is read but discarded.
func loadSkillMeta(ctx context.Context, client S3API, bucket, key string) (*Skill, error) {
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get %s: %w", key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", key, err)
	}
	return parseMeta(string(data))
}

func loadSkill(ctx context.Context, client S3API, bucket, key string) (*Skill, error) {
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get %s: %w", key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", key, err)
	}
	return parse(string(data))
}

// parseMeta extracts only the YAML frontmatter from a SKILL.md, ignoring the body.
func parseMeta(raw string) (*Skill, error) {
	frontmatter, _, err := splitFrontmatter(raw)
	if err != nil {
		return nil, err
	}
	var sk Skill
	if err := yaml.Unmarshal([]byte(frontmatter), &sk); err != nil {
		return nil, fmt.Errorf("failed to parse frontmatter: %w", err)
	}
	return &sk, nil
}

// parse splits a SKILL.md into YAML frontmatter and markdown body.
func parse(raw string) (*Skill, error) {
	frontmatter, body, err := splitFrontmatter(raw)
	if err != nil {
		return nil, err
	}
	var sk Skill
	if err := yaml.Unmarshal([]byte(frontmatter), &sk); err != nil {
		return nil, fmt.Errorf("failed to parse frontmatter: %w", err)
	}
	sk.Content = strings.TrimSpace(body)
	return &sk, nil
}

// splitFrontmatter extracts YAML between --- delimiters and the remaining body.
func splitFrontmatter(raw string) (frontmatter, body string, err error) {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	// find opening ---
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "---" {
			break
		}
	}
	// collect frontmatter until closing ---
	var fm strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		fm.WriteString(line)
		fm.WriteString("\n")
	}
	if fm.Len() == 0 {
		return "", "", fmt.Errorf("no frontmatter found")
	}
	// remainder is body
	var b strings.Builder
	for scanner.Scan() {
		b.WriteString(scanner.Text())
		b.WriteString("\n")
	}
	return fm.String(), b.String(), nil
}

// slugFromKey extracts the directory name from a key like "skills/naming-conventions/SKILL.md".
func slugFromKey(key string) string {
	key = strings.TrimPrefix(key, "skills/")
	key = strings.TrimSuffix(key, "/SKILL.md")
	return key
}
