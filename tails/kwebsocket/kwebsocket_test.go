package kwebsocket_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	kitsune "github.com/jonathan/go-kitsune"
	"github.com/jonathan/go-kitsune/tails/kwebsocket"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// echoServer accepts a WebSocket connection, reads n JSON messages, echos each
// one back, then closes the connection.
func echoServer(n int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		for i := 0; i < n; i++ {
			var msg any
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}
			if err := wsjson.Write(ctx, conn, msg); err != nil {
				return
			}
		}
		conn.Close(websocket.StatusNormalClosure, "done")
	})
}

func TestReadWrite(t *testing.T) {
	srv := httptest.NewServer(echoServer(3))
	defer srv.Close()

	ctx := context.Background()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	serverConn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverConn.CloseNow()

	type msg struct {
		N int `json:"n"`
	}

	// Send 3 messages then read 3 echoes concurrently.
	errCh := make(chan error, 1)
	go func() {
		sink := kwebsocket.Write[msg](serverConn, func(m msg) (websocket.MessageType, []byte, error) {
			b, err := json.Marshal(m)
			return websocket.MessageText, b, err
		})
		for i := 0; i < 3; i++ {
			if err := sink(ctx, msg{N: i}); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- nil
	}()

	got, err := kitsune.Map(
		kwebsocket.Read(serverConn, func(_ websocket.MessageType, b []byte) (msg, error) {
			var m msg
			return m, json.Unmarshal(b, &m)
		}),
		func(_ context.Context, m msg) (int, error) { return m.N, nil },
	).Collect(ctx)

	if writeErr := <-errCh; writeErr != nil {
		t.Fatalf("write error: %v", writeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
}
