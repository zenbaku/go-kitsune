module github.com/zenbaku/go-kitsune

go 1.26.1

require (
	github.com/zenbaku/go-kitsune/hooks v0.0.0
	golang.org/x/sync v0.20.0
	golang.org/x/time v0.15.0
)

require pgregory.net/rapid v1.2.0 // indirect

replace github.com/zenbaku/go-kitsune/hooks => ./hooks
