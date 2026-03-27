module github.com/jonathan/go-kitsune/examples/http

go 1.26.1

require (
	github.com/jonathan/go-kitsune v0.0.0
	github.com/jonathan/go-kitsune/tails/khttp v0.0.0
)

require golang.org/x/sync v0.20.0 // indirect

replace (
	github.com/jonathan/go-kitsune => ../..
	github.com/jonathan/go-kitsune/tails/khttp => ../../tails/khttp
)
