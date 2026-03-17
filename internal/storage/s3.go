package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/ellistarn/muse/internal/awsconfig"
	"github.com/ellistarn/muse/internal/conversation"
)

// S3Store implements Store backed by an S3 bucket.
type S3Store struct {
	s3     *s3.Client
	bucket string
}

// Verify S3Store implements Store at compile time.
var _ Store = (*S3Store)(nil)

func NewS3Store(ctx context.Context, bucket string) (*S3Store, error) {
	cfg, err := awsconfig.Load(ctx)
	if err != nil {
		return nil, err
	}
	return &S3Store{
		s3:     s3.NewFromConfig(cfg),
		bucket: bucket,
	}, nil
}

// ListSessions returns all session keys with their S3 LastModified timestamps.
func (c *S3Store) ListSessions(ctx context.Context) ([]SessionEntry, error) {
	var entries []SessionEntry
	paginator := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: &c.bucket,
		Prefix: aws.String("conversations/"),
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

// PutSession uploads a session as JSON and returns the number of bytes written.
func (c *S3Store) PutSession(ctx context.Context, session *conversation.Session) (int, error) {
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("failed to marshal session: %w", err)
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
		return 0, fmt.Errorf("failed to upload session %s: %w", session.SessionID, err)
	}
	return len(data), nil
}

// GetSession downloads and deserializes a session from S3.
func (c *S3Store) GetSession(ctx context.Context, src, sessionID string) (*conversation.Session, error) {
	key := sessionKey(src, sessionID)
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, wrapS3NotFound(err, key)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read session %s: %w", sessionID, err)
	}
	var session conversation.Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session %s: %w", sessionID, err)
	}
	return &session, nil
}

// GetMuse returns the latest muse version by finding the most recent timestamp.
func (c *S3Store) GetMuse(ctx context.Context) (string, error) {
	timestamps, err := c.ListMuses(ctx)
	if err != nil {
		return "", err
	}
	// Walk backwards to find the latest timestamp that has a muse.md
	for i := len(timestamps) - 1; i >= 0; i-- {
		content, err := c.GetMuseVersion(ctx, timestamps[i])
		if err == nil {
			return content, nil
		}
	}
	return "", &NotFoundError{Key: "muse/versions/"}
}

// PutMuse writes a muse version at the given timestamp.
func (c *S3Store) PutMuse(ctx context.Context, timestamp, content string) error {
	key := museVersionKey(timestamp)
	contentType := "text/markdown"
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &c.bucket,
		Key:         &key,
		Body:        bytes.NewReader([]byte(content)),
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("failed to put muse: %w", err)
	}
	return nil
}

// PutMuseDiff writes a diff summary at the given timestamp.
func (c *S3Store) PutMuseDiff(ctx context.Context, timestamp, content string) error {
	key := museDiffKey(timestamp)
	contentType := "text/markdown"
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &c.bucket,
		Key:         &key,
		Body:        bytes.NewReader([]byte(content)),
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("failed to put muse diff: %w", err)
	}
	return nil
}

// GetMuseDiff reads the diff summary for the given timestamp.
func (c *S3Store) GetMuseDiff(ctx context.Context, timestamp string) (string, error) {
	key := museDiffKey(timestamp)
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	})
	if err != nil {
		return "", wrapS3NotFound(err, key)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read muse diff %s: %w", timestamp, err)
	}
	return string(data), nil
}

// ListMuses returns timestamps of all muse versions, sorted ascending.
func (c *S3Store) ListMuses(ctx context.Context) ([]string, error) {
	prefix := "muse/versions/"
	out, err := c.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:    &c.bucket,
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list muse versions: %w", err)
	}
	var timestamps []string
	for _, cp := range out.CommonPrefixes {
		p := aws.ToString(cp.Prefix)
		p = strings.TrimPrefix(p, prefix)
		p = strings.TrimSuffix(p, "/")
		if p != "" {
			timestamps = append(timestamps, p)
		}
	}
	sort.Strings(timestamps)
	return timestamps, nil
}

// GetMuseVersion downloads a specific muse version from S3.
func (c *S3Store) GetMuseVersion(ctx context.Context, timestamp string) (string, error) {
	key := museVersionKey(timestamp)
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	})
	if err != nil {
		return "", wrapS3NotFound(err, key)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read muse version %s: %w", timestamp, err)
	}
	return string(data), nil
}

// PutObservation writes observations to S3 under observations/{source}/{session}.md.
func (c *S3Store) PutObservation(ctx context.Context, key, content string) error {
	path := observationKey(key)
	contentType := "text/markdown"
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &c.bucket,
		Key:         &path,
		Body:        bytes.NewReader([]byte(content)),
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("failed to put observation for %s: %w", key, err)
	}
	return nil
}

// ListObservations returns the keys of all persisted observations under observations/.
func (c *S3Store) ListObservations(ctx context.Context) (map[string]time.Time, error) {
	observations := map[string]time.Time{}
	paginator := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: &c.bucket,
		Prefix: aws.String("observations/"),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list observations: %w", err)
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			conversationKey := observationKeyToConversationKey(key)
			observations[conversationKey] = aws.ToTime(obj.LastModified)
		}
	}
	return observations, nil
}

// GetObservation downloads an observation's content from S3.
func (c *S3Store) GetObservation(ctx context.Context, conversationKey string) (string, error) {
	path := observationKey(conversationKey)
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &path,
	})
	if err != nil {
		return "", wrapS3NotFound(err, conversationKey)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read observation for %s: %w", conversationKey, err)
	}
	return string(data), nil
}

// DeletePrefix removes all objects under a given S3 prefix.
func (c *S3Store) DeletePrefix(ctx context.Context, prefix string) error {
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

// wrapS3NotFound converts S3 NoSuchKey errors to NotFoundError.
func wrapS3NotFound(err error, key string) error {
	var nsk *s3types.NoSuchKey
	if errors.As(err, &nsk) {
		return &NotFoundError{Key: key}
	}
	return err
}

func sessionKey(src, sessionID string) string {
	return fmt.Sprintf("conversations/%s/%s.json", src, sessionID)
}

// parseSessionKey extracts source and session ID from a key like "conversations/opencode/ses_abc.json".
func parseSessionKey(key string) (src, sessionID string) {
	key = strings.TrimPrefix(key, "conversations/")
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

func museVersionKey(timestamp string) string {
	return fmt.Sprintf("muse/versions/%s/muse.md", timestamp)
}

func museDiffKey(timestamp string) string {
	return fmt.Sprintf("muse/versions/%s/diff.md", timestamp)
}

// observationKey converts a conversation key to its observation storage key.
// conversations/opencode/ses_abc.json -> observations/opencode/ses_abc.md
func observationKey(conversationKey string) string {
	return fmt.Sprintf("observations/%s.md", strings.TrimPrefix(strings.TrimSuffix(conversationKey, ".json"), "conversations/"))
}

// observationKeyToConversationKey converts an observation storage key back to a conversation key.
// observations/opencode/ses_abc.md -> conversations/opencode/ses_abc.json
func observationKeyToConversationKey(key string) string {
	rel := strings.TrimPrefix(key, "observations/")
	return "conversations/" + strings.TrimSuffix(rel, ".md") + ".json"
}
