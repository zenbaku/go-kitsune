package khttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	kitsune "github.com/jonathan/go-kitsune"
	"github.com/jonathan/go-kitsune/tails/khttp"
)

func TestGetPages(t *testing.T) {
	// Simulate a 3-page paginated API.
	pages := []struct {
		items []string
		next  string
	}{
		{[]string{"a", "b"}, "/page2"},
		{[]string{"c", "d"}, "/page3"},
		{[]string{"e"}, ""},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var idx int
		switch r.URL.Path {
		case "/page1":
			idx = 0
		case "/page2":
			idx = 1
		case "/page3":
			idx = 2
		default:
			http.NotFound(w, r)
			return
		}
		resp := struct {
			Items []string `json:"items"`
			Next  string   `json:"next"`
		}{
			Items: pages[idx].items,
		}
		if pages[idx].next != "" {
			resp.Next = "http://" + r.Host + pages[idx].next
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	type pageResp struct {
		Items []string `json:"items"`
		Next  string   `json:"next"`
	}

	p := khttp.GetPages(srv.Client(), srv.URL+"/page1",
		func(resp *http.Response) ([]string, string, error) {
			var pr pageResp
			if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
				return nil, "", err
			}
			return pr.Items, pr.Next, nil
		},
	)

	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"a", "b", "c", "d", "e"}
	if len(results) != len(expected) {
		t.Fatalf("expected %d items, got %d: %v", len(expected), len(results), results)
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("item %d = %q, want %q", i, v, expected[i])
		}
	}
}

func TestPost(t *testing.T) {
	var received []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = append(received, strings.TrimSpace(string(body)))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	input := kitsune.FromSlice([]string{"hello", "world"})
	err := input.ForEach(khttp.Post(srv.Client(), srv.URL, "text/plain",
		func(s string) (io.Reader, error) {
			return strings.NewReader(s), nil
		},
	)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(received) != 2 || received[0] != "hello" || received[1] != "world" {
		t.Fatalf("unexpected received: %v", received)
	}
}

func TestPostJSON(t *testing.T) {
	type Item struct {
		Name string `json:"name"`
	}

	var received []Item
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var item Item
		json.NewDecoder(r.Body).Decode(&item)
		received = append(received, item)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	input := kitsune.FromSlice([]Item{{Name: "alice"}, {Name: "bob"}})
	err := input.ForEach(khttp.Post(srv.Client(), srv.URL, "application/json",
		func(item Item) (io.Reader, error) {
			data, err := json.Marshal(item)
			return bytes.NewReader(data), err
		},
	)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(received) != 2 || received[0].Name != "alice" {
		t.Fatalf("unexpected: %v", received)
	}
}

func TestPostHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	input := kitsune.FromSlice([]string{"test"})
	err := input.ForEach(khttp.Post(srv.Client(), srv.URL, "text/plain",
		func(s string) (io.Reader, error) { return strings.NewReader(s), nil },
	)).Run(context.Background())

	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	httpErr, ok := err.(*khttp.HTTPError)
	if !ok {
		t.Fatalf("expected HTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != 500 {
		t.Fatalf("expected status 500, got %d", httpErr.StatusCode)
	}
}

func TestGetPagesE2E(t *testing.T) {
	// Full pipeline: paginated GET → transform → POST.
	getSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		type resp struct {
			Items []int  `json:"items"`
			Next  string `json:"next"`
		}
		var out resp
		if r.URL.Path == "/data" {
			out = resp{Items: []int{1, 2, 3}, Next: "http://" + r.Host + "/data2"}
		} else {
			out = resp{Items: []int{4, 5}}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer getSrv.Close()

	var posted []int
	postSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n int
		json.NewDecoder(r.Body).Decode(&n)
		posted = append(posted, n)
		w.WriteHeader(200)
	}))
	defer postSrv.Close()

	type pageResp struct {
		Items []int  `json:"items"`
		Next  string `json:"next"`
	}

	source := khttp.GetPages(getSrv.Client(), getSrv.URL+"/data",
		func(resp *http.Response) ([]int, string, error) {
			var pr pageResp
			json.NewDecoder(resp.Body).Decode(&pr)
			return pr.Items, pr.Next, nil
		},
	)

	doubled := kitsune.Map(source, func(_ context.Context, n int) (int, error) {
		return n * 10, nil
	})

	err := doubled.ForEach(khttp.Post(postSrv.Client(), postSrv.URL, "application/json",
		func(n int) (io.Reader, error) {
			data, _ := json.Marshal(n)
			return bytes.NewReader(data), nil
		},
	)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	expected := []int{10, 20, 30, 40, 50}
	if len(posted) != len(expected) {
		t.Fatalf("expected %d posts, got %d: %v", len(expected), len(posted), posted)
	}
	for i, v := range posted {
		if v != expected[i] {
			t.Errorf("posted[%d] = %d, want %d", i, v, expected[i])
		}
	}
}
