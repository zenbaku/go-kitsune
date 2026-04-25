// Example: DevStore captures each Segment's output on the first run and
// replays from snapshot on subsequent runs, letting you iterate on a
// downstream segment without re-running expensive upstream work.
//
//	go run ./examples/devstore
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/zenbaku/go-kitsune"
)

type Page struct {
	URL  string
	Body string
}

func main() {
	dir, err := os.MkdirTemp("", "kitsune-devstore-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	store := kitsune.NewFileDevStore(dir)

	fetch := kitsune.Stage[string, Page](func(urls *kitsune.Pipeline[string]) *kitsune.Pipeline[Page] {
		return kitsune.Map(urls, func(_ context.Context, url string) (Page, error) {
			fmt.Printf("  fetch  %s\n", url)
			return Page{URL: url, Body: "body of " + url}, nil
		})
	})
	enrich := kitsune.Stage[Page, Page](func(p *kitsune.Pipeline[Page]) *kitsune.Pipeline[Page] {
		return kitsune.Map(p, func(_ context.Context, page Page) (Page, error) {
			fmt.Printf("  enrich %s\n", page.URL)
			page.Body = page.Body + " (enriched)"
			return page, nil
		})
	})

	fetchSeg := kitsune.NewSegment("fetch", fetch)
	enrichSeg := kitsune.NewSegment("enrich", enrich)

	runOnce := func(label string) {
		fmt.Printf("--- %s ---\n", label)
		urls := kitsune.FromSlice([]string{"https://a", "https://b"})
		pipeline := kitsune.Then(fetchSeg, enrichSeg)
		runner := pipeline.Apply(urls).
			ForEach(func(_ context.Context, page Page) error {
				fmt.Printf("  result %s -> %s\n", page.URL, page.Body)
				return nil
			})
		if _, err := runner.Run(context.Background(), kitsune.WithDevStore(store)); err != nil {
			panic(err)
		}
	}

	runOnce("first run (captures snapshots)")
	fmt.Println()
	runOnce("second run (replays snapshots; no fetch/enrich logs)")
}
