// Example: enrich: combine a stream of events with user records fetched in
// bulk, using BatchTimeout to bound latency under low throughput.
//
// Enrich is like LookupBy but calls a Join function to produce the output type
// directly, avoiding an intermediate Pair. Setting BatchTimeout on EnrichConfig
// flushes partial batches after a duration so the pipeline does not stall when
// traffic is slow.
//
// Demonstrates:
//   - EnrichConfig with BatchSize, BatchTimeout, and a Join function
//   - A simulated user database fetched in bulk
//   - Join producing a typed EnrichedEvent
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// Event is the incoming record; UserID drives the lookup.
type Event struct {
	ID     int
	UserID int
	Action string
}

// User is the fetched record.
type User struct {
	ID   int
	Name string
}

// EnrichedEvent is the output: an Event annotated with its User's name.
type EnrichedEvent struct {
	EventID int
	UserID  int
	Action  string
	Name    string
}

var users = map[int]User{
	10: {ID: 10, Name: "alice"},
	11: {ID: 11, Name: "bob"},
	12: {ID: 12, Name: "carol"},
}

func fetchUsers(_ context.Context, ids []int) (map[int]User, error) {
	// Simulate a bulk lookup round-trip.
	time.Sleep(2 * time.Millisecond)
	out := make(map[int]User, len(ids))
	for _, id := range ids {
		if u, ok := users[id]; ok {
			out[id] = u
		}
	}
	return out, nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := kitsune.NewChannel[Event](8)

	cfg := kitsune.EnrichConfig[Event, int, User, EnrichedEvent]{
		Key:   func(e Event) int { return e.UserID },
		Fetch: fetchUsers,
		Join: func(e Event, u User) EnrichedEvent {
			return EnrichedEvent{
				EventID: e.ID,
				UserID:  e.UserID,
				Action:  e.Action,
				Name:    u.Name,
			}
		},
		BatchSize:    50,
		BatchTimeout: 25 * time.Millisecond,
	}

	done := make(chan error, 1)
	go func() {
		_, err := kitsune.Enrich(ch.Source(), cfg).
			ForEach(func(_ context.Context, e EnrichedEvent) error {
				fmt.Printf("  event=%d user=%d (%s) action=%s\n", e.EventID, e.UserID, e.Name, e.Action)
				return nil
			}).Run(ctx)
		done <- err
	}()

	fmt.Println("=== enrich with BatchTimeout ===")
	events := []Event{
		{ID: 1, UserID: 10, Action: "login"},
		{ID: 2, UserID: 11, Action: "click"},
		{ID: 3, UserID: 12, Action: "purchase"},
	}
	for _, e := range events {
		if err := ch.Send(ctx, e); err != nil {
			panic(err)
		}
		// Trickle: gap longer than BatchTimeout forces a partial flush per item.
		time.Sleep(40 * time.Millisecond)
	}

	ch.Close()
	if err := <-done; err != nil {
		panic(err)
	}
}
