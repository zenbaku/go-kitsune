package kitsune

import (
	"context"

	"github.com/jonathan/go-kitsune/internal/engine"
)

// From creates a Pipeline that reads from an existing channel.
// The pipeline completes when the channel is closed.
func From[T any](ch <-chan T) *Pipeline[T] {
	g := engine.New()
	fn := func(ctx context.Context, yield func(any) bool) error {
		for {
			select {
			case item, ok := <-ch:
				if !ok {
					return nil
				}
				if !yield(item) {
					return nil
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	id := g.AddNode(&engine.Node{Kind: engine.Source, Fn: fn})
	return &Pipeline[T]{g: g, node: id}
}

// FromSlice creates a Pipeline that emits each element of the slice.
func FromSlice[T any](items []T) *Pipeline[T] {
	g := engine.New()
	fn := func(ctx context.Context, yield func(any) bool) error {
		for _, item := range items {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !yield(item) {
				return nil
			}
		}
		return nil
	}
	id := g.AddNode(&engine.Node{Kind: engine.Source, Fn: fn})
	return &Pipeline[T]{g: g, node: id}
}

// Generate creates a Pipeline from a push-based source function.
// Call yield for each item. yield returns false if the pipeline is done
// (cancelled or downstream signalled completion). Generate handles
// backpressure internally — yield blocks when downstream is full.
//
//	kitsune.Generate(func(ctx context.Context, yield func(Record) bool) error {
//	    for cursor := ""; ; {
//	        page, next, err := api.Fetch(ctx, cursor)
//	        if err != nil { return err }
//	        for _, r := range page {
//	            if !yield(r) { return nil }
//	        }
//	        if next == "" { return nil }
//	        cursor = next
//	    }
//	})
func Generate[T any](fn func(ctx context.Context, yield func(T) bool) error) *Pipeline[T] {
	g := engine.New()
	wrapped := func(ctx context.Context, yield func(any) bool) error {
		return fn(ctx, func(item T) bool { return yield(item) })
	}
	id := g.AddNode(&engine.Node{Kind: engine.Source, Fn: wrapped})
	return &Pipeline[T]{g: g, node: id}
}
