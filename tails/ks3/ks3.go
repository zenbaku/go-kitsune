// Package ks3 provides S3-compatible object storage source helpers for kitsune pipelines.
//
// Users own the S3 client — configure credentials, region, and endpoint yourself.
// Kitsune will never create or close clients. The same client works with AWS S3,
// Google Cloud Storage (via the S3-interop API), MinIO, and other S3-compatible stores.
//
// List and parse all objects under a prefix:
//
//	cfg, _ := config.LoadDefaultConfig(ctx)
//	client := s3.NewFromConfig(cfg)
//
//	pipe := ks3.ListObjects(client, "my-bucket", "data/", func(key string, body io.Reader) (Event, error) {
//	    return parseNDJSON(body)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Stream lines from a single object:
//
//	pipe := ks3.Lines(client, "my-bucket", "data/large-file.txt")
//	pipe.ForEach(processLine).Run(ctx)
package ks3

import (
	"bufio"
	"context"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	kitsune "github.com/zenbaku/go-kitsune"
)

// S3Client is the subset of the AWS S3 API used by this package.
// *s3.Client satisfies this interface; it can also be stubbed in tests.
type S3Client interface {
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// ListObjects creates a Pipeline that lists objects under prefix in bucket,
// downloads each one, and calls parse to convert the object body into a value
// of type T. All objects are visited in lexicographic key order.
// parse is responsible for closing the body if it consumes it partially.
//
// Objects up to 1000 keys per page are listed via ListObjectsV2 pagination.
func ListObjects[T any](client S3Client, bucket, prefix string, parse func(key string, body io.Reader) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		paginator := &listPager{client: client, bucket: bucket, prefix: prefix}
		for {
			keys, done, err := paginator.next(ctx)
			if err != nil {
				return err
			}
			for _, key := range keys {
				out, err := client.GetObject(ctx, &s3.GetObjectInput{
					Bucket: aws.String(bucket),
					Key:    aws.String(key),
				})
				if err != nil {
					return err
				}
				v, err := parse(key, out.Body)
				out.Body.Close()
				if err != nil {
					return err
				}
				if !yield(v) {
					return nil
				}
			}
			if done {
				return nil
			}
		}
	})
}

// Lines creates a Pipeline that streams the lines of a single S3 object,
// one string per line (newline stripped). Empty lines are included.
func Lines(client S3Client, bucket, key string) *kitsune.Pipeline[string] {
	return kitsune.Generate(func(ctx context.Context, yield func(string) bool) error {
		out, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return err
		}
		defer out.Body.Close()

		scanner := bufio.NewScanner(out.Body)
		for scanner.Scan() {
			if !yield(scanner.Text()) {
				return nil
			}
			if ctx.Err() != nil {
				return nil
			}
		}
		return scanner.Err()
	})
}

// listPager tracks pagination state for ListObjectsV2.
type listPager struct {
	client        S3Client
	bucket        string
	prefix        string
	continuationToken *string
	started       bool
}

func (p *listPager) next(ctx context.Context) (keys []string, done bool, err error) {
	if p.started && p.continuationToken == nil {
		return nil, true, nil
	}
	p.started = true

	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(p.bucket),
		Prefix: aws.String(p.prefix),
	}
	if p.continuationToken != nil {
		input.ContinuationToken = p.continuationToken
	}

	out, err := p.client.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, false, err
	}

	for _, obj := range out.Contents {
		if obj.Key != nil {
			keys = append(keys, *obj.Key)
		}
	}

	if out.IsTruncated != nil && *out.IsTruncated {
		p.continuationToken = out.NextContinuationToken
		return keys, false, nil
	}
	p.continuationToken = nil
	return keys, true, nil
}
