// Package kes provides Elasticsearch / OpenSearch source and sink helpers for
// kitsune pipelines.
//
// The caller owns the [elasticsearch.Client]: configure addresses, auth, and
// TLS yourself. Kitsune will never create or close clients.
//
// Scrolling search source:
//
//	cfg := elasticsearch.Config{Addresses: []string{"http://localhost:9200"}}
//	client, _ := elasticsearch.NewClient(cfg)
//
//	query := map[string]any{"query": map[string]any{"match_all": map[string]any{}}}
//	pipe := kes.Search(client, "my-index", query, func(hit []byte) (Event, error) {
//	    var e Event
//	    return e, json.Unmarshal(hit, &e)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Bulk index sink:
//
//	kitsune.Batch(pipe, 1000).
//	    ForEach(kes.Bulk(client, "my-index", func(e Event) (id string, doc []byte, err error) {
//	        b, err := json.Marshal(e)
//	        return e.ID, b, err
//	    })).Run(ctx)
//
// Delivery semantics: Search is a read-only scrolling source (at-most-once;
// the scroll is cleared on pipeline exit). Bulk is a synchronous write: the
// call returns after the batch is indexed. Per-item errors inside a bulk
// response terminate the pipeline with the first error found.
package kes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	kitsune "github.com/zenbaku/go-kitsune"
)

// Search creates a Pipeline that streams hits from an Elasticsearch scrolling
// search. The query is a map that is marshalled to JSON and sent as the
// request body. unmarshal receives the raw JSON of each hit's "_source" field.
//
// The scroll is cleared when the pipeline ends. The client is not closed when
// the pipeline ends; the caller owns it.
func Search[T any](client *elasticsearch.Client, index string, query map[string]any, unmarshal func([]byte) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		body, err := json.Marshal(query)
		if err != nil {
			return err
		}

		res, err := client.Search(
			client.Search.WithContext(ctx),
			client.Search.WithIndex(index),
			client.Search.WithBody(bytes.NewReader(body)),
			client.Search.WithScroll(2*time.Minute),
			client.Search.WithSize(100),
		)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.IsError() {
			return fmt.Errorf("kes: Search: %s", res.Status())
		}

		var scrollID string
		defer func() {
			if scrollID != "" {
				_, _ = client.ClearScroll(
					client.ClearScroll.WithScrollID(scrollID),
				)
			}
		}()

		for {
			var result struct {
				ScrollID string `json:"_scroll_id"`
				Hits     struct {
					Hits []struct {
						Source json.RawMessage `json:"_source"`
					} `json:"hits"`
				} `json:"hits"`
			}
			if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
				return err
			}
			_ = res.Body.Close()

			scrollID = result.ScrollID

			if len(result.Hits.Hits) == 0 {
				return nil
			}

			for _, hit := range result.Hits.Hits {
				v, err := unmarshal(hit.Source)
				if err != nil {
					return err
				}
				if !yield(v) {
					return nil
				}
			}

			// Advance the scroll.
			res, err = client.Scroll(
				client.Scroll.WithContext(ctx),
				client.Scroll.WithScrollID(scrollID),
				client.Scroll.WithScroll(2*time.Minute),
			)
			if err != nil {
				return err
			}
			if res.IsError() {
				_ = res.Body.Close()
				return fmt.Errorf("kes: Scroll: %s", res.Status())
			}
		}
	})
}

// Bulk returns a batch sink function that indexes items using the Elasticsearch
// Bulk API. marshal converts each item into an id and a JSON document body.
// A non-empty id produces an index action with explicit _id; an empty id lets
// Elasticsearch auto-assign one. Use with [kitsune.Pipeline.ForEach] after
// [kitsune.Batch].
//
// The client is not closed when the pipeline ends; the caller owns it.
func Bulk[T any](client *elasticsearch.Client, index string, marshal func(T) (id string, doc []byte, err error)) func(context.Context, []T) error {
	return func(ctx context.Context, items []T) error {
		var buf bytes.Buffer
		for _, item := range items {
			id, doc, err := marshal(item)
			if err != nil {
				return err
			}
			// Action line.
			action := map[string]any{"index": map[string]any{"_index": index}}
			if id != "" {
				action["index"].(map[string]any)["_id"] = id
			}
			actionJSON, err := json.Marshal(action)
			if err != nil {
				return err
			}
			buf.Write(actionJSON)
			buf.WriteByte('\n')
			buf.Write(doc)
			buf.WriteByte('\n')
		}

		res, err := client.Bulk(
			bytes.NewReader(buf.Bytes()),
			client.Bulk.WithContext(ctx),
			client.Bulk.WithIndex(index),
		)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.IsError() {
			return fmt.Errorf("kes: Bulk: %s: %s", res.Status(), body)
		}

		// Check for per-item errors.
		var bulkRes struct {
			Errors bool `json:"errors"`
			Items  []map[string]struct {
				Error *struct {
					Type   string `json:"type"`
					Reason string `json:"reason"`
				} `json:"error"`
			} `json:"items"`
		}
		if err := json.Unmarshal(body, &bulkRes); err != nil {
			return err
		}
		if bulkRes.Errors {
			for _, item := range bulkRes.Items {
				for _, v := range item {
					if v.Error != nil {
						return fmt.Errorf("kes: Bulk item error: %s: %s", v.Error.Type, v.Error.Reason)
					}
				}
			}
		}
		return nil
	}
}
