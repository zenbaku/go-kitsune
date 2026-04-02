module github.com/zenbaku/go-kitsune/bench

go 1.26.1

require (
	github.com/zenbaku/go-kitsune v0.0.0
	github.com/reugn/go-streams v0.13.0
	github.com/sourcegraph/conc v0.3.0
	golang.org/x/sync v0.20.0
)

require golang.org/x/time v0.15.0 // indirect

replace github.com/zenbaku/go-kitsune => ../
