module github.com/jonathan/go-kitsune/tails/kwebsocket

go 1.26.1

require (
	github.com/jonathan/go-kitsune v0.0.0
	nhooyr.io/websocket v1.8.17
)

require golang.org/x/sync v0.20.0 // indirect

replace github.com/jonathan/go-kitsune => ../..
