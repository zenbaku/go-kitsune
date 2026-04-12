module github.com/zenbaku/go-kitsune/tails/kazeh

go 1.26.1

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.18.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.11.1 // indirect
	github.com/Azure/go-amqp v1.4.0 // indirect
	github.com/zenbaku/go-kitsune/hooks v0.1.0 // indirect
	golang.org/x/net v0.41.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.26.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/messaging/azeventhubs v1.4.0
	github.com/zenbaku/go-kitsune v0.0.0
)

replace github.com/zenbaku/go-kitsune => ../..

replace github.com/zenbaku/go-kitsune/hooks => ../../hooks
