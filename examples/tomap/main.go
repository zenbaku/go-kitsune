// Example: tomap: buffer a stream into map[K]V, then collect with Single.
//
// Demonstrates: ToMap, Single.
package main

import (
	"context"
	"fmt"
	"log"

	kitsune "github.com/zenbaku/go-kitsune"
)

type user struct {
	ID   int
	Name string
}

func main() {
	ctx := context.Background()
	users := kitsune.FromSlice([]user{
		{1, "alice"}, {2, "bob"}, {3, "carol"},
	})

	byID, err := kitsune.Single(ctx,
		kitsune.ToMap(users,
			func(u user) int { return u.ID },
			func(u user) string { return u.Name },
		),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("by id: %v\n", byID)

	// Last-writer-wins on duplicate keys.
	dupes := kitsune.FromSlice([]user{
		{1, "first"}, {1, "second"}, {1, "third"},
	})
	byID2, err := kitsune.Single(ctx,
		kitsune.ToMap(dupes,
			func(u user) int { return u.ID },
			func(u user) string { return u.Name },
		),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("last wins: %v\n", byID2)
}
