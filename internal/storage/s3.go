package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ellistarn/shade/internal/source"
)

type Client struct {
	s3     *s3.Client
	bucket string
}

func NewClient(ctx context.Context, bucket string) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-west-2"))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return &Client{
		s3:     s3.NewFromConfig(cfg),
		bucket: bucket,
	}, nil
}

// SessionEntry is the metadata returned by ListSessions without downloading full content.
type SessionEntry struct {
	Source       string
	SessionID    string
	Key          string
	LastModified time.Time
}

// ListSessions returns all session keys with their S3 LastModified timestamps.
func (c *Client) ListSessions(ctx context.Context) ([]SessionEntry, error) {
	var entries []SessionEntry
	paginator := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: &c.bucket,
		Prefix: aws.String("memories/"),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list S3 objects: %w", err)
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			src, id := parseSessionKey(key)
			if src == "" {
				continue
			}
			entries = append(entries, SessionEntry{
				Source:       src,
				SessionID:    id,
				Key:          key,
				LastModified: aws.ToTime(obj.LastModified),
			})
		}
	}
	return entries, nil
}

// PutSession uploads a session as JSON.
func (c *Client) PutSession(ctx context.Context, session *source.Session) error {
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}
	key := sessionKey(session.Source, session.SessionID)
	contentType := "application/json"
	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &c.bucket,
		Key:         &key,
		Body:        bytes.NewReader(data),
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("failed to upload session %s: %w", session.SessionID, err)
	}
	return nil
}

// GetSession downloads and deserializes a session from S3.
func (c *Client) GetSession(ctx context.Context, src, sessionID string) (*source.Session, error) {
	key := sessionKey(src, sessionID)
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get session %s: %w", sessionID, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read session %s: %w", sessionID, err)
	}
	var session source.Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session %s: %w", sessionID, err)
	}
	return &session, nil
}

// GetJSON downloads and unmarshals a JSON object from S3.
func (c *Client) GetJSON(ctx context.Context, key string, v any) error {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	})
	if err != nil {
		return fmt.Errorf("failed to get %s: %w", key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", key, err)
	}
	return json.Unmarshal(data, v)
}

// PutJSON marshals and uploads a JSON object to S3.
func (c *Client) PutJSON(ctx context.Context, key string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal %s: %w", key, err)
	}
	contentType := "application/json"
	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &c.bucket,
		Key:         &key,
		Body:        bytes.NewReader(data),
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("failed to put %s: %w", key, err)
	}
	return nil
}

// PutSkill writes a SKILL.md file to S3 under skills/{name}/SKILL.md.
func (c *Client) PutSkill(ctx context.Context, name, content string) error {
	key := fmt.Sprintf("skills/%s/SKILL.md", name)
	contentType := "text/markdown"
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &c.bucket,
		Key:         &key,
		Body:        bytes.NewReader([]byte(content)),
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("failed to put skill %s: %w", name, err)
	}
	return nil
}

// DeletePrefix removes all objects under a given S3 prefix.
func (c *Client) DeletePrefix(ctx context.Context, prefix string) error {
	paginator := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: &c.bucket,
		Prefix: &prefix,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to list %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			if _, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: &c.bucket,
				Key:    &key,
			}); err != nil {
				return fmt.Errorf("failed to delete %s: %w", key, err)
			}
		}
	}
	return nil
}

func sessionKey(src, sessionID string) string {
	return fmt.Sprintf("memories/%s/%s.json", src, sessionID)
}

// parseSessionKey extracts source and session ID from a key like "memories/opencode/ses_abc.json".
func parseSessionKey(key string) (src, sessionID string) {
	// key format: memories/{source}/{session_id}.json
	key = strings.TrimPrefix(key, "memories/")
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	src = parts[0]
	sessionID = strings.TrimSuffix(parts[1], ".json")
	if src == "" || sessionID == "" {
		return "", ""
	}
	return src, sessionID
}
