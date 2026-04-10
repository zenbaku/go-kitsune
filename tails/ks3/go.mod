module github.com/zenbaku/go-kitsune/tails/ks3

go 1.26.1

replace github.com/zenbaku/go-kitsune => ../..

require (
	github.com/aws/aws-sdk-go-v2 v1.32.6
	github.com/aws/aws-sdk-go-v2/service/s3 v1.71.0
	github.com/zenbaku/go-kitsune v0.0.0-00010101000000-000000000000
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.6.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.3.25 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.6.25 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.3.25 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.12.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.4.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.12.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.18.6 // indirect
	github.com/aws/smithy-go v1.22.1 // indirect
	github.com/zenbaku/go-kitsune/hooks v0.1.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)
