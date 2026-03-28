// Package kgrpc provides gRPC streaming source and sink helpers for kitsune
// pipelines.
//
// Recv adapts a server-streaming RPC into a kitsune source:
//
//	conn, _ := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
//	client := pb.NewMyServiceClient(conn)
//	stream, _ := client.StreamEvents(ctx, &pb.StreamEventsRequest{})
//
//	kgrpc.Recv(stream).
//	    Map(transform).
//	    ForEach(handle).
//	    Run(ctx)
//
// Send adapts a client-streaming RPC into a kitsune sink:
//
//	stream, _ := client.UploadEvents(ctx)
//	kitsune.FromSlice(events).
//	    ForEach(kgrpc.Send(stream)).
//	    Run(ctx)
package kgrpc

import (
	"context"
	"io"

	kitsune "github.com/jonathan/go-kitsune"
	"google.golang.org/grpc"
)

// Recv creates a Pipeline that reads messages from a gRPC server-streaming
// RPC. Each message is yielded as a pointer (matching gRPC's generated types).
// The pipeline ends when the stream is exhausted (io.EOF) or the context is
// cancelled.
//
// The stream is not closed when the pipeline ends — the caller owns it.
func Recv[T any](stream grpc.ServerStreamingClient[T]) *kitsune.Pipeline[*T] {
	return kitsune.Generate(func(ctx context.Context, yield func(*T) bool) error {
		for {
			msg, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					return nil
				}
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			if !yield(msg) {
				return nil
			}
		}
	})
}

// Send returns a sink function that writes each item to a gRPC client-streaming
// RPC. Items are sent as pointers (matching gRPC's generated types).
// Use with [kitsune.Pipeline.ForEach].
//
// The stream is not closed when the pipeline ends — the caller owns it.
func Send[T any, R any](stream grpc.ClientStreamingClient[T, R]) func(context.Context, *T) error {
	return func(_ context.Context, item *T) error {
		return stream.Send(item)
	}
}
