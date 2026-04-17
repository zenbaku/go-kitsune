// Package kwebsocket provides WebSocket source and sink helpers for kitsune
// pipelines.
//
// The caller owns the [websocket.Conn]: dial or accept connections yourself.
// Kitsune will never create or close connections.
//
// Read frames from a WebSocket connection:
//
//	conn, _, err := websocket.Dial(ctx, "wss://example.com/feed", nil)
//	if err != nil { ... }
//	defer conn.CloseNow()
//
//	pipe := kwebsocket.Read(conn, func(typ websocket.MessageType, b []byte) (Event, error) {
//	    var e Event
//	    return e, json.Unmarshal(b, &e)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Write frames to a WebSocket connection:
//
//	kitsune.FromSlice(events).
//	    ForEach(kwebsocket.Write(conn, func(e Event) (websocket.MessageType, []byte, error) {
//	        b, err := json.Marshal(e)
//	        return websocket.MessageText, b, err
//	    })).Run(ctx)
//
// Delivery semantics: at-most-once. WebSocket is a streaming transport with
// no application-level ack. A frame sent by Write is delivered to the
// connection layer; whether the remote end processes it before a crash is
// outside the pipeline's control.
package kwebsocket

import (
	"context"

	kitsune "github.com/zenbaku/go-kitsune"
	"nhooyr.io/websocket"
)

// Read creates a Pipeline that reads WebSocket frames from conn. unmarshal is
// called for each frame with the message type and raw bytes; it returns the
// decoded value. The pipeline ends when the connection is closed normally or
// the context is cancelled.
//
// The connection is not closed when the pipeline ends; the caller owns it.
func Read[T any](conn *websocket.Conn, unmarshal func(websocket.MessageType, []byte) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		for {
			typ, b, err := conn.Read(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				// Normal close is not an error.
				if websocket.CloseStatus(err) != -1 {
					return nil
				}
				return err
			}
			v, err := unmarshal(typ, b)
			if err != nil {
				return err
			}
			if !yield(v) {
				return nil
			}
		}
	})
}

// Write returns a sink function that writes each item as a WebSocket frame.
// marshal converts the item into a message type and byte payload. Use with
// [kitsune.Pipeline.ForEach].
//
// The connection is not closed when the pipeline ends; the caller owns it.
func Write[T any](conn *websocket.Conn, marshal func(T) (websocket.MessageType, []byte, error)) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		typ, b, err := marshal(item)
		if err != nil {
			return err
		}
		return conn.Write(ctx, typ, b)
	}
}
