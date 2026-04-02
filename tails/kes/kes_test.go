package kes_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/zenbaku/go-kitsune/tails/kes"
)

// cannedTransport returns a sequence of canned HTTP responses.
type cannedTransport struct {
	responses []*http.Response
	pos       int
}

func (t *cannedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The ES client sends a GET / for the product check on first use.
	if req.URL.Path == "/" {
		return infoResponse(), nil
	}
	if t.pos >= len(t.responses) {
		// Return an empty scroll result to terminate pagination.
		body := `{"_scroll_id":"","hits":{"hits":[]}}`
		return jsonResponse(body), nil
	}
	res := t.responses[t.pos]
	t.pos++
	return res, nil
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Type":      []string{"application/json"},
			"X-Elastic-Product": []string{"Elasticsearch"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

// infoResponse is returned for the initial ES product check request.
func infoResponse() *http.Response {
	body := `{"name":"test","cluster_name":"test","version":{"number":"8.0.0"},"tagline":"You Know, for Search"}`
	return jsonResponse(body)
}

func TestSearch(t *testing.T) {
	type Doc struct {
		N int `json:"n"`
	}

	// First response: 3 hits + scroll ID. Second response: empty (end of scroll).
	transport := &cannedTransport{responses: []*http.Response{
		jsonResponse(`{
			"_scroll_id": "scroll-1",
			"hits": {
				"hits": [
					{"_source": {"n": 1}},
					{"_source": {"n": 2}},
					{"_source": {"n": 3}}
				]
			}
		}`),
	}}

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := kes.Search(client, "test-index",
		map[string]any{"query": map[string]any{"match_all": map[string]any{}}},
		func(hit []byte) (Doc, error) {
			var d Doc
			return d, json.Unmarshal(hit, &d)
		},
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	for i, d := range got {
		if d.N != i+1 {
			t.Errorf("doc[%d].N: want %d, got %d", i, i+1, d.N)
		}
	}
}

func TestBulk(t *testing.T) {
	type Doc struct {
		ID    string `json:"id"`
		Value int    `json:"value"`
	}

	// Capture the bulk body via a custom RoundTripper.
	var captured []byte
	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Transport: roundTripFn(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/" {
				return infoResponse(), nil
			}
			captured, _ = io.ReadAll(req.Body)
			resp := `{"took":1,"errors":false,"items":[{"index":{"_id":"1","status":201}},{"index":{"_id":"2","status":201}}]}`
			return jsonResponse(resp), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	docs := []Doc{{ID: "1", Value: 42}, {ID: "2", Value: 99}}
	sink := kes.Bulk(client, "test-index", func(d Doc) (string, []byte, error) {
		b, err := json.Marshal(d)
		return d.ID, b, err
	})
	if err := sink(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	if len(captured) == 0 {
		t.Error("expected Bulk request body to be captured")
	}
	// Verify NDJSON format: each pair of lines is action + doc.
	lines := bytes.Split(bytes.TrimSpace(captured), []byte("\n"))
	if len(lines) != 4 { // 2 docs × 2 lines each
		t.Errorf("expected 4 NDJSON lines, got %d", len(lines))
	}
}

type roundTripFn func(*http.Request) (*http.Response, error)

func (f roundTripFn) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestIntegration(t *testing.T) {
	if os.Getenv("ELASTICSEARCH_URL") == "" {
		t.Skip("ELASTICSEARCH_URL not set; skipping Elasticsearch integration tests")
	}
}
