package ks3_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/ks3"
)

// ---------------------------------------------------------------------------
// In-memory stub that satisfies ks3.S3Client
// ---------------------------------------------------------------------------

type stubClient struct {
	objects map[string]string // key → body
}

func (c *stubClient) ListObjectsV2(_ context.Context, params *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	prefix := ""
	if params.Prefix != nil {
		prefix = *params.Prefix
	}
	var contents []s3types.Object
	for key := range c.objects {
		if strings.HasPrefix(key, prefix) {
			k := key
			contents = append(contents, s3types.Object{Key: &k})
		}
	}
	return &s3.ListObjectsV2Output{
		Contents:    contents,
		IsTruncated: aws.Bool(false),
	}, nil
}

func (c *stubClient) GetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	body := c.objects[*params.Key]
	return &s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(body)),
	}, nil
}

// ---------------------------------------------------------------------------
// Unit tests (no AWS credentials required)
// ---------------------------------------------------------------------------

func TestListObjects(t *testing.T) {
	client := &stubClient{
		objects: map[string]string{
			"data/a.txt": "hello",
			"data/b.txt": "world",
			"other/c.txt": "skip",
		},
	}

	var got []string
	pipe := ks3.ListObjects[string](client, "bucket", "data/",
		func(_ string, body io.Reader) (string, error) {
			b, err := io.ReadAll(body)
			return string(b), err
		},
	)
	results, err := pipe.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got = results
	if len(got) != 2 {
		t.Fatalf("expected 2 objects, got %d: %v", len(got), got)
	}
}

func TestLines(t *testing.T) {
	client := &stubClient{
		objects: map[string]string{
			"file.txt": "line1\nline2\nline3",
		},
	}

	results, err := ks3.Lines(client, "bucket", "file.txt").
		Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(results))
	}
	if results[0] != "line1" || results[1] != "line2" || results[2] != "line3" {
		t.Fatalf("unexpected lines: %v", results)
	}
}

func TestListObjectsCollect(t *testing.T) {
	client := &stubClient{
		objects: map[string]string{
			"prefix/1.txt": "a",
			"prefix/2.txt": "b",
			"prefix/3.txt": "c",
		},
	}

	pipe := ks3.ListObjects[string](client, "bucket", "prefix/",
		func(_ string, body io.Reader) (string, error) {
			b, _ := io.ReadAll(body)
			return string(b), nil
		},
	)
	mapped := kitsune.Map(pipe, func(_ context.Context, s string) (string, error) {
		return strings.ToUpper(s), nil
	})
	results, err := mapped.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Integration test (requires AWS credentials)
// ---------------------------------------------------------------------------

func integrationBucket(t *testing.T) string {
	t.Helper()
	bucket := os.Getenv("TEST_S3_BUCKET")
	if bucket == "" {
		t.Skip("TEST_S3_BUCKET not set; skipping S3 integration test")
	}
	return bucket
}
