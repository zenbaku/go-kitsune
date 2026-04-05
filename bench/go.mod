module github.com/zenbaku/go-kitsune/bench

go 1.26.1

require (
	github.com/reugn/go-streams v0.13.0
	github.com/sourcegraph/conc v0.3.0
	github.com/zenbaku/go-kitsune v0.0.0
	github.com/zenbaku/go-kitsune/v2 v2.0.0-local
	golang.org/x/sync v0.20.0
)

require (
	github.com/zenbaku/go-kitsune/hooks v0.0.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace (
	github.com/zenbaku/go-kitsune => ../
	github.com/zenbaku/go-kitsune/hooks => ../hooks
	github.com/zenbaku/go-kitsune/v2 => ../v2
)
