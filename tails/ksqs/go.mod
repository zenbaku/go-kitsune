module github.com/zenbaku/go-kitsune/tails/ksqs

go 1.26.1

require (
	github.com/aws/aws-sdk-go-v2 v1.36.4
	github.com/aws/aws-sdk-go-v2/service/sqs v1.38.7
	github.com/zenbaku/go-kitsune v0.0.0
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.3.35 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.6.35 // indirect
	github.com/aws/smithy-go v1.22.2 // indirect
	github.com/zenbaku/go-kitsune/hooks v0.0.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace github.com/zenbaku/go-kitsune => ../..

replace github.com/zenbaku/go-kitsune/hooks => ../../hooks
