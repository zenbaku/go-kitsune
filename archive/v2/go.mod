module github.com/zenbaku/go-kitsune/v2

go 1.25.0

require golang.org/x/sync v0.10.0

require (
	github.com/zenbaku/go-kitsune/hooks v0.0.0
	golang.org/x/time v0.15.0 // indirect
)

replace github.com/zenbaku/go-kitsune/hooks => ../hooks
