// Example: compose — reusable pipeline fragments with Through.
//
// Demonstrates: Through for middleware-style composition.
package main

import (
	"context"
	"fmt"

	kitsune "github.com/jonathan/go-kitsune"
)

type Request struct {
	User    string
	Path    string
	Authed  bool
	Allowed bool
}

// Authenticate is a reusable pipeline fragment that marks requests as authenticated.
func Authenticate(p *kitsune.Pipeline[Request]) *kitsune.Pipeline[Request] {
	return kitsune.Map(p, func(_ context.Context, r Request) (Request, error) {
		r.Authed = r.User != ""
		return r, nil
	}).Filter(func(r Request) bool {
		return r.Authed
	})
}

// Authorize is a reusable fragment that checks path permissions.
func Authorize(allowed map[string]bool) func(*kitsune.Pipeline[Request]) *kitsune.Pipeline[Request] {
	return func(p *kitsune.Pipeline[Request]) *kitsune.Pipeline[Request] {
		return kitsune.Map(p, func(_ context.Context, r Request) (Request, error) {
			r.Allowed = allowed[r.Path]
			return r, nil
		}).Filter(func(r Request) bool {
			return r.Allowed
		})
	}
}

func main() {
	requests := []Request{
		{User: "alice", Path: "/admin"},
		{User: "", Path: "/public"}, // no user → dropped by Authenticate
		{User: "bob", Path: "/public"},
		{User: "carol", Path: "/secret"}, // not in allowed paths → dropped
	}

	allowedPaths := map[string]bool{"/admin": true, "/public": true}

	input := kitsune.FromSlice(requests)
	handled := input.
		Through(Authenticate).
		Through(Authorize(allowedPaths)).
		Tap(func(r Request) {
			fmt.Printf("  ✓ %s → %s\n", r.User, r.Path)
		})

	results, err := handled.Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("\n%d requests passed all middleware\n", len(results))
}
