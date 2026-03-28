// Example: websocket — reading and writing WebSocket frames with kitsune.
//
// Demonstrates: kwebsocket.Read (source), kwebsocket.Write (sink), an
// in-process echo server, and a pipeline that reads, transforms, and writes
// frames back over a second connection.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	kitsune "github.com/jonathan/go-kitsune"
	"github.com/jonathan/go-kitsune/tails/kwebsocket"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

type Event struct {
	ID    int    `json:"id"`
	Value string `json:"value"`
}

func main() {
	ctx := context.Background()

	// --- Example 1: Read from an echo server ---
	fmt.Println("=== Read from echo server ===")

	// Start an in-process server that echoes 5 JSON events then closes.
	events := []Event{
		{1, "alpha"}, {2, "beta"}, {3, "gamma"}, {4, "delta"}, {5, "epsilon"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for _, e := range events {
			if err := wsjson.Write(r.Context(), conn, e); err != nil {
				return
			}
		}
		conn.Close(websocket.StatusNormalClosure, "done")
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.CloseNow()

	received, err := kwebsocket.Read(conn, func(_ websocket.MessageType, b []byte) (Event, error) {
		var e Event
		return e, json.Unmarshal(b, &e)
	}).Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, e := range received {
		fmt.Printf("  received: id=%d value=%q\n", e.ID, e.Value)
	}

	// --- Example 2: Write to a collecting server ---
	fmt.Println("\n=== Write to collecting server ===")

	collected := make(chan Event, 10)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for i := 0; i < 3; i++ {
			var e Event
			if err := wsjson.Read(r.Context(), conn, &e); err != nil {
				return
			}
			collected <- e
		}
	}))
	defer srv2.Close()

	ws2URL := "ws" + strings.TrimPrefix(srv2.URL, "http")
	conn2, _, err := websocket.Dial(ctx, ws2URL, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer conn2.CloseNow()

	toSend := []Event{{10, "x"}, {20, "y"}, {30, "z"}}
	err = kitsune.Map(
		kitsune.FromSlice(toSend),
		func(_ context.Context, e Event) (Event, error) {
			e.Value = strings.ToUpper(e.Value) // transform in-flight
			return e, nil
		},
		kitsune.WithName("uppercase"),
	).ForEach(kwebsocket.Write(conn2, func(e Event) (websocket.MessageType, []byte, error) {
		b, err := json.Marshal(e)
		return websocket.MessageText, b, err
	})).Run(ctx)
	if err != nil {
		log.Fatal(err)
	}
	conn2.Close(websocket.StatusNormalClosure, "")

	close(collected)
	for e := range collected {
		fmt.Printf("  server received: id=%d value=%q\n", e.ID, e.Value)
	}
}
