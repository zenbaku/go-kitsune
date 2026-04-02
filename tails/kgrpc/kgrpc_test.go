package kgrpc_test

import (
	"context"
	"io"
	"testing"

	"github.com/zenbaku/go-kitsune/tails/kgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// intMsg is a plain struct used as a proto-like message type in tests.
type intMsg struct{ N int }

// stubServerStream simulates a gRPC server-streaming RPC over a slice.
type stubServerStream struct {
	items []*intMsg
	pos   int
}

func (s *stubServerStream) Recv() (*intMsg, error) {
	if s.pos >= len(s.items) {
		return nil, io.EOF
	}
	v := s.items[s.pos]
	s.pos++
	return v, nil
}

func (s *stubServerStream) Header() (metadata.MD, error) { return nil, nil }
func (s *stubServerStream) Trailer() metadata.MD          { return nil }
func (s *stubServerStream) CloseSend() error              { return nil }
func (s *stubServerStream) Context() context.Context      { return context.Background() }
func (s *stubServerStream) SendMsg(m any) error           { return nil }
func (s *stubServerStream) RecvMsg(m any) error           { return nil }

var _ grpc.ServerStreamingClient[intMsg] = (*stubServerStream)(nil)

func TestRecv(t *testing.T) {
	stream := &stubServerStream{items: []*intMsg{{1}, {2}, {3}}}
	got, err := kgrpc.Recv(stream).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	for i, v := range got {
		if v.N != i+1 {
			t.Errorf("item[%d]: want %d, got %d", i, i+1, v.N)
		}
	}
}

func TestRecvStopsEarly(t *testing.T) {
	stream := &stubServerStream{items: []*intMsg{{1}, {2}, {3}, {4}, {5}}}
	got, err := kgrpc.Recv(stream).Take(2).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
}

// stubClientStream simulates a gRPC client-streaming RPC.
type stubClientStream struct {
	sent []*intMsg
}

func (s *stubClientStream) Send(v *intMsg) error {
	s.sent = append(s.sent, v)
	return nil
}
func (s *stubClientStream) CloseAndRecv() (*intMsg, error) { return &intMsg{}, nil }
func (s *stubClientStream) Header() (metadata.MD, error)   { return nil, nil }
func (s *stubClientStream) Trailer() metadata.MD           { return nil }
func (s *stubClientStream) CloseSend() error               { return nil }
func (s *stubClientStream) Context() context.Context       { return context.Background() }
func (s *stubClientStream) SendMsg(m any) error            { return nil }
func (s *stubClientStream) RecvMsg(m any) error            { return nil }

var _ grpc.ClientStreamingClient[intMsg, intMsg] = (*stubClientStream)(nil)

func TestSend(t *testing.T) {
	stream := &stubClientStream{}
	items := []*intMsg{{1}, {2}, {3}}

	ctx := context.Background()
	sink := kgrpc.Send[intMsg, intMsg](stream)
	for _, item := range items {
		if err := sink(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if len(stream.sent) != 3 {
		t.Fatalf("want 3 sent, got %d", len(stream.sent))
	}
	for i, v := range stream.sent {
		if v.N != i+1 {
			t.Errorf("sent[%d]: want %d, got %d", i, i+1, v.N)
		}
	}
}
