module github.com/zenbaku/go-kitsune/examples/files

go 1.26.1

require (
	github.com/zenbaku/go-kitsune v0.0.0
	github.com/zenbaku/go-kitsune/tails/kfile v0.0.0
)

require (
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace (
	github.com/zenbaku/go-kitsune => ../..
	github.com/zenbaku/go-kitsune/tails/kfile => ../../tails/kfile
)
