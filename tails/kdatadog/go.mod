module github.com/zenbaku/go-kitsune/tails/kdatadog

go 1.26.1

require (
	github.com/DataDog/datadog-go/v5 v5.6.0
	github.com/zenbaku/go-kitsune v0.0.0
	github.com/zenbaku/go-kitsune/hooks v0.1.0
)

require (
	github.com/Microsoft/go-winio v0.5.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.0.0-20210510120138-977fb7262007 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace github.com/zenbaku/go-kitsune => ../..

