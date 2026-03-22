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

// ListConversations returns all conversation keys with their S3 LastModified timestamps.
func (c *S3Store) ListConversations(ctx context.Context) ([]ConversationEntry, error) {
	var entries []ConversationEntry
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
			src, id := parseConversationKey(key)
			if src == "" {
				continue
			}
			entries = append(entries, ConversationEntry{
				Source:         src,
				ConversationID: id,
				Key:            key,
				LastModified:   aws.ToTime(obj.LastModified),
			})
		}
	}
	return entries, nil
}

// PutConversation uploads a conversation as JSON and returns the number of bytes written.
func (c *S3Store) PutConversation(ctx context.Context, conv *conversation.Conversation) (int, error) {
	data, err := json.MarshalIndent(conv, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("failed to marshal conversation: %w", err)
	}
	key := conversationKey(conv.Source, conv.ConversationID)
	contentType := "application/json"
	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &c.bucket,
		Key:         &key,
		Body:        bytes.NewReader(data),
		ContentType: &contentType,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to upload conversation %s: %w", conv.ConversationID, err)
	}
	return len(data), nil
}

// GetConversation downloads and deserializes a conversation from S3.
func (c *S3Store) GetConversation(ctx context.Context, src, conversationID string) (*conversation.Conversation, error) {
	key := conversationKey(src, conversationID)
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
		return nil, fmt.Errorf("failed to read conversation %s: %w", conversationID, err)
	}
	var conv conversation.Conversation
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, fmt.Errorf("failed to unmarshal conversation %s: %w", conversationID, err)
	}
	if err := conv.Validate(); err != nil {
		return nil, fmt.Errorf("invalid conversation %s: %w", conversationID, err)
	}
	return &conv, nil
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
	return "", &NotFoundError{Key: "versions/"}
}

// PutMuse writes a muse version at the given timestamp and updates the stable
// latest key at muse.md in the bucket root for external consumers.
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
	stableKey := "muse.md"
	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &c.bucket,
		Key:         &stableKey,
		Body:        bytes.NewReader([]byte(content)),
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("failed to put stable muse: %w", err)
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
	prefix := "versions/"
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

// PutObservation writes observations to S3 under observations/{source}/{conversation}.md.
func (c *S3Store) PutObservation(ctx context.Context, key, content string) error {
	path := ObservationKey(key)
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
			conversationKey := ObservationKeyToConversationKey(key)
			observations[conversationKey] = aws.ToTime(obj.LastModified)
		}
	}
	return observations, nil
}

// GetObservation downloads an observation's content from S3.
func (c *S3Store) GetObservation(ctx context.Context, conversationKey string) (string, error) {
	path := ObservationKey(conversationKey)
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

// PutData writes raw bytes to S3 at the given key.
func (c *S3Store) PutData(ctx context.Context, key string, data []byte) error {
	contentType := "application/octet-stream"
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
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

// GetData reads raw bytes from S3 at the given key.
func (c *S3Store) GetData(ctx context.Context, key string) ([]byte, error) {
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
		return nil, fmt.Errorf("failed to read %s: %w", key, err)
	}
	return data, nil
}

// ListData returns all keys under the given S3 prefix.
func (c *S3Store) ListData(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: &c.bucket,
		Prefix: &prefix,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			keys = append(keys, aws.ToString(obj.Key))
		}
	}
	return keys, nil
}

// DeleteData removes all objects under the given S3 prefix.
func (c *S3Store) DeleteData(ctx context.Context, prefix string) error {
	return c.DeletePrefix(ctx, prefix)
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

func conversationKey(src, conversationID string) string {
	return fmt.Sprintf("conversations/%s/%s.json", src, conversationID)
}

// parseConversationKey extracts source and conversation ID from a key like "conversations/opencode/ses_abc.json".
func parseConversationKey(key string) (src, conversationID string) {
	key = strings.TrimPrefix(key, "conversations/")
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	src = parts[0]
	conversationID = strings.TrimSuffix(parts[1], ".json")
	if src == "" || conversationID == "" {
		return "", ""
	}
	return src, conversationID
}

func museVersionKey(timestamp string) string {
	return fmt.Sprintf("versions/%s/muse.md", timestamp)
}

func museDiffKey(timestamp string) string {
	return fmt.Sprintf("versions/%s/diff.md", timestamp)
}

// ObservationKey converts a conversation key to its observation storage key.
// conversations/opencode/ses_abc.json -> observations/opencode/ses_abc.md
func ObservationKey(conversationKey string) string {
	return fmt.Sprintf("observations/%s.md", strings.TrimPrefix(strings.TrimSuffix(conversationKey, ".json"), "conversations/"))
}

// ObservationKeyToConversationKey converts an observation storage key back to a conversation key.
// observations/opencode/ses_abc.md -> conversations/opencode/ses_abc.json
func ObservationKeyToConversationKey(key string) string {
	rel := strings.TrimPrefix(key, "observations/")
	return "conversations/" + strings.TrimSuffix(rel, ".md") + ".json"
}
