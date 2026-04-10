module github.com/zenbaku/go-kitsune/tails/kes

go 1.26.1

require (
	github.com/elastic/go-elasticsearch/v8 v8.17.0
	github.com/zenbaku/go-kitsune v0.0.0
)

require (
	github.com/elastic/elastic-transport-go/v8 v8.6.0 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/zenbaku/go-kitsune/hooks v0.1.0 // indirect
	go.opentelemetry.io/otel v1.28.0 // indirect
	go.opentelemetry.io/otel/metric v1.28.0 // indirect
	go.opentelemetry.io/otel/trace v1.28.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace github.com/zenbaku/go-kitsune => ../..
