module github.com/zenbaku/go-kitsune/tails/kprometheus

go 1.26.1

require (
	github.com/prometheus/client_golang v1.22.0
	github.com/prometheus/client_model v0.6.1
	github.com/zenbaku/go-kitsune v0.0.0
	github.com/zenbaku/go-kitsune/hooks v0.1.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/common v0.62.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.5 // indirect
)

replace github.com/zenbaku/go-kitsune => ../..

