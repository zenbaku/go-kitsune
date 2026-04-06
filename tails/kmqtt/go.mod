module github.com/zenbaku/go-kitsune/tails/kmqtt

go 1.26.1

require (
	github.com/eclipse/paho.mqtt.golang v1.5.0
	github.com/zenbaku/go-kitsune v0.0.0
)

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/zenbaku/go-kitsune/hooks v0.0.0 // indirect
	golang.org/x/net v0.27.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace github.com/zenbaku/go-kitsune => ../..

replace github.com/zenbaku/go-kitsune/hooks => ../../hooks
