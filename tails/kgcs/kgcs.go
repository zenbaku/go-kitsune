// Package kgcs provides Google Cloud Storage source and sink helpers for
// kitsune pipelines.
//
// The caller owns the GCS client: configure credentials yourself and pass the
// result to [NewClient]. Kitsune will never create or close clients.
//
// List and parse all objects under a prefix:
//
//	client, _ := storage.NewClient(ctx)
//	gcs := kgcs.NewClient(client)
//
//	pipe := kgcs.ListObjects(gcs, "my-bucket", "data/", func(key string, body io.Reader) (Event, error) {
//	    return parseNDJSON(body)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Stream lines from a single object:
//
//	pipe := kgcs.Lines(gcs, "my-bucket", "data/large-file.txt")
//	pipe.ForEach(processLine).Run(ctx)
//
// Upload items to GCS:
//
//	pipe.ForEach(kgcs.Upload(gcs, "my-bucket",
//	    func(e Event) string { return "events/" + e.ID + ".json" },
//	    func(e Event, w io.Writer) error { return json.NewEncoder(w).Encode(e) },
//	)).Run(ctx)
//
// Delivery semantics: ListObjects and Lines are read-only sources (at-most-once;
// no ack mechanism). Upload writes synchronously: the object is finalised when
// the write function returns without error and the internal writer is closed.
package kgcs

import (
	"bufio"
	"context"
	"errors"
	"io"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	kitsune "github.com/zenbaku/go-kitsune"
)

// Done is returned by [ObjectIterator.Next] when there are no more objects.
var Done = errors.New("kgcs: no more objects")

// GCSClient is the subset of the Cloud Storage API used by this package.
// Use [NewClient] to wrap a real *storage.Client; the interface can also be
// satisfied by a stub in tests.
type GCSClient interface {
	// ListObjects returns an iterator over object names under prefix in bucket.
	ListObjects(ctx context.Context, bucket, prefix string) ObjectIterator
	// NewReader opens the named object for reading. The caller must close it.
	NewReader(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	// NewWriter opens the named object for writing. The caller must close it
	// to finalise the upload; close errors must be checked.
	NewWriter(ctx context.Context, bucket, key string) (io.WriteCloser, error)
}

// ObjectIterator iterates over GCS object names.
// Next returns the next name, or [Done] when exhausted.
type ObjectIterator interface {
	Next() (name string, err error)
}

// NewClient wraps a *storage.Client to implement [GCSClient].
func NewClient(c *storage.Client) GCSClient {
	return &gcsAdapter{c: c}
}

// ListObjects creates a Pipeline that lists objects under prefix in bucket,
// downloads each one, and calls parse to convert the body into a value of
// type T. Objects are visited in lexicographic name order.
// parse is responsible for draining or closing body if it reads it partially.
func ListObjects[T any](client GCSClient, bucket, prefix string, parse func(key string, body io.Reader) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		it := client.ListObjects(ctx, bucket, prefix)
		for {
			key, err := it.Next()
			if errors.Is(err, Done) {
				return nil
			}
			if err != nil {
				return err
			}
			r, err := client.NewReader(ctx, bucket, key)
			if err != nil {
				return err
			}
			v, parseErr := parse(key, r)
			r.Close()
			if parseErr != nil {
				return parseErr
			}
			if !yield(v) {
				return nil
			}
		}
	})
}

// Lines creates a Pipeline that streams the lines of a single GCS object,
// one string per line (newline stripped). Empty lines are included.
func Lines(client GCSClient, bucket, key string) *kitsune.Pipeline[string] {
	return kitsune.Generate(func(ctx context.Context, yield func(string) bool) error {
		r, err := client.NewReader(ctx, bucket, key)
		if err != nil {
			return err
		}
		defer r.Close()

		scanner := bufio.NewScanner(r)
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

// Upload returns a ForEach-compatible function that uploads each item to GCS.
// key returns the object name for an item; write serialises the item into the
// provided writer. The GCS object is finalised when write returns without
// error and the internal writer is closed successfully. If write returns an
// error the incomplete upload is abandoned (Close is still called to release
// resources, but its error is suppressed).
func Upload[T any](client GCSClient, bucket string, key func(T) string, write func(T, io.Writer) error) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		w, err := client.NewWriter(ctx, bucket, key(item))
		if err != nil {
			return err
		}
		if err := write(item, w); err != nil {
			w.Close() //nolint:errcheck // abandoning upload; write error takes precedence
			return err
		}
		return w.Close()
	}
}

// ---------------------------------------------------------------------------
// Real client adapter
// ---------------------------------------------------------------------------

type gcsAdapter struct {
	c *storage.Client
}

func (a *gcsAdapter) ListObjects(ctx context.Context, bucket, prefix string) ObjectIterator {
	it := a.c.Bucket(bucket).Objects(ctx, &storage.Query{Prefix: prefix})
	return &gcsIterator{it: it}
}

func (a *gcsAdapter) NewReader(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	return a.c.Bucket(bucket).Object(key).NewReader(ctx)
}

func (a *gcsAdapter) NewWriter(ctx context.Context, bucket, key string) (io.WriteCloser, error) {
	return a.c.Bucket(bucket).Object(key).NewWriter(ctx), nil
}

type gcsIterator struct {
	it *storage.ObjectIterator
}

func (i *gcsIterator) Next() (string, error) {
	attrs, err := i.it.Next()
	if err == iterator.Done {
		return "", Done
	}
	if err != nil {
		return "", err
	}
	return attrs.Name, nil
}
