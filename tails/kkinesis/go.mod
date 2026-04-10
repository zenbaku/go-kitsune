module github.com/zenbaku/go-kitsune/tails/kkinesis

go 1.26.1

require (
	github.com/aws/aws-sdk-go-v2 v1.32.7
	github.com/aws/aws-sdk-go-v2/service/kinesis v1.32.9
	github.com/zenbaku/go-kitsune v0.0.0
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.6.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.3.26 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.6.26 // indirect
	github.com/aws/smithy-go v1.22.1 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/zenbaku/go-kitsune/hooks v0.1.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace github.com/zenbaku/go-kitsune => ../..
