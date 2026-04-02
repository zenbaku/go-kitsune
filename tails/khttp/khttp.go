// Package khttp provides HTTP source and sink helpers for kitsune pipelines.
//
// Users own the [http.Client] — configure timeouts, transport, and
// authentication yourself. Kitsune will never create or close clients.
package khttp

import (
	"context"
	"io"
	"net/http"

	kitsune "github.com/zenbaku/go-kitsune"
)

// GetPages creates a Pipeline that follows paginated HTTP responses.
// Starting from firstURL, it calls parse on each response to extract items
// and the next page URL. Pagination stops when nextURL is empty.
//
//	khttp.GetPages(client, "https://api.example.com/users?page=1",
//	    func(resp *http.Response) ([]User, string, error) {
//	        var page PageResponse
//	        json.NewDecoder(resp.Body).Decode(&page)
//	        return page.Users, page.NextURL, nil
//	    },
//	)
func GetPages[T any](client *http.Client, firstURL string, parse func(resp *http.Response) (items []T, nextURL string, err error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		url := firstURL
		for url != "" {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return err
			}
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			items, next, err := parse(resp)
			resp.Body.Close()
			if err != nil {
				return err
			}
			for _, item := range items {
				if !yield(item) {
					return nil
				}
			}
			url = next
		}
		return nil
	})
}

// Post returns a sink function that POSTs each item as an HTTP request.
// The marshal function converts the item into a request body.
// Use with [kitsune.Pipeline.ForEach].
func Post[T any](client *http.Client, url string, contentType string, marshal func(T) (io.Reader, error)) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		body, err := marshal(item)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", contentType)
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return &HTTPError{StatusCode: resp.StatusCode, Status: resp.Status}
		}
		return nil
	}
}

// HTTPError is returned when an HTTP response has a 4xx or 5xx status code.
type HTTPError struct {
	StatusCode int
	Status     string
}

func (e *HTTPError) Error() string { return e.Status }
