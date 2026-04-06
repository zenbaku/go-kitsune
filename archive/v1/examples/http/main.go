// Example: http — paginated GET source and POST sink.
//
// Demonstrates: khttp.GetPages (paginated API), khttp.Post (webhook sink).
// Uses httptest for a self-contained demo — no real server needed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/khttp"
)

type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type PageResponse struct {
	Users   []User `json:"users"`
	NextURL string `json:"next_url,omitempty"`
}

func main() {
	ctx := context.Background()

	// --- Simulate a paginated API server ---
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		var resp PageResponse
		switch page {
		case "", "1":
			resp = PageResponse{
				Users:   []User{{1, "Alice"}, {2, "Bob"}},
				NextURL: "http://" + r.Host + "/users?page=2",
			}
		case "2":
			resp = PageResponse{
				Users:   []User{{3, "Carol"}, {4, "Dave"}},
				NextURL: "http://" + r.Host + "/users?page=3",
			}
		default:
			resp = PageResponse{
				Users: []User{{5, "Eve"}},
				// No NextURL — last page.
			}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer apiServer.Close()

	// --- Simulate a webhook receiver ---
	var received []string
	webhookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = append(received, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookServer.Close()

	// --- Pipeline: paginated GET → transform → POST to webhook ---
	fmt.Println("=== Paginated GET → transform → POST ===")

	source := khttp.GetPages(apiServer.Client(), apiServer.URL+"/users?page=1",
		func(resp *http.Response) ([]User, string, error) {
			var pr PageResponse
			if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
				return nil, "", err
			}
			return pr.Users, pr.NextURL, nil
		},
	)

	// Transform: greet each user.
	greeted := kitsune.Map(source, func(_ context.Context, u User) (string, error) {
		return fmt.Sprintf("Hello, %s (#%d)!", u.Name, u.ID), nil
	})

	// Sink: POST each greeting to the webhook.
	err := greeted.ForEach(khttp.Post(webhookServer.Client(), webhookServer.URL, "text/plain",
		func(s string) (io.Reader, error) {
			return bytes.NewReader([]byte(s)), nil
		},
	)).Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Sent %d webhooks:\n", len(received))
	for _, msg := range received {
		fmt.Printf("  %s\n", msg)
	}
}
