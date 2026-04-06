module github.com/zenbaku/go-kitsune/tails/kazsb

go 1.26.1

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.18.2 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.11.2 // indirect
	github.com/Azure/go-amqp v1.4.0 // indirect
	github.com/zenbaku/go-kitsune/hooks v0.0.0 // indirect
	golang.org/x/net v0.42.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.27.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus v1.10.0
	github.com/zenbaku/go-kitsune v0.0.0
)

replace github.com/zenbaku/go-kitsune => ../..

replace github.com/zenbaku/go-kitsune/hooks => ../../hooks
