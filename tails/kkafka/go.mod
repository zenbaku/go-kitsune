module github.com/zenbaku/go-kitsune/tails/kkafka

go 1.26.1

replace github.com/zenbaku/go-kitsune => ../..

require (
	github.com/segmentio/kafka-go v0.4.47
	github.com/zenbaku/go-kitsune v0.0.0-00010101000000-000000000000
)

require (
	github.com/klauspost/compress v1.15.9 // indirect
	github.com/pierrec/lz4/v4 v4.1.15 // indirect
	github.com/zenbaku/go-kitsune/hooks v0.1.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace github.com/zenbaku/go-kitsune/hooks => ../../hooks
