// Package kmqtt provides MQTT source and sink helpers for kitsune pipelines.
//
// Users own the [mqtt.Client] — configure brokers, credentials, and TLS
// yourself. Kitsune will never create or disconnect clients.
//
// Subscribe source:
//
//	opts := mqtt.NewClientOptions().AddBroker("tcp://localhost:1883")
//	client := mqtt.NewClient(opts)
//	client.Connect().Wait()
//	defer client.Disconnect(250)
//
//	pipe := kmqtt.Subscribe(client, "sensors/#", 1, func(msg mqtt.Message) (Reading, error) {
//	    var r Reading
//	    return r, json.Unmarshal(msg.Payload(), &r)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Publish sink:
//
//	kitsune.FromSlice(readings).
//	    ForEach(kmqtt.Publish(client, "sensors/data", 1, func(r Reading) ([]byte, error) {
//	        return json.Marshal(r)
//	    })).Run(ctx)
//
// MQTT uses a callback-based subscription model. This package bridges it to
// kitsune's Generate pattern via an internal buffered channel:
//  1. The callback is registered with client.Subscribe.
//  2. Received messages are decoded and sent to the channel.
//  3. The Generate loop reads from the channel and yields.
//  4. When the context is cancelled or yield returns false, the subscription
//     is unsubscribed and the pipeline exits.
package kmqtt

import (
	"context"

	kitsune "github.com/jonathan/go-kitsune"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Subscribe creates a Pipeline that receives messages from an MQTT topic.
// unmarshal is called for each message; a decode error terminates the pipeline.
// qos specifies the MQTT QoS level (0, 1, or 2).
//
// The client is not disconnected when the pipeline ends — the caller owns it.
func Subscribe[T any](client mqtt.Client, topic string, qos byte, unmarshal func(mqtt.Message) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		type result struct {
			val T
			err error
		}
		ch := make(chan result, 32)

		token := client.Subscribe(topic, qos, func(_ mqtt.Client, msg mqtt.Message) {
			v, err := unmarshal(msg)
			select {
			case ch <- result{val: v, err: err}:
			case <-ctx.Done():
			}
		})
		token.Wait()
		if err := token.Error(); err != nil {
			return err
		}
		defer func() {
			client.Unsubscribe(topic).Wait() //nolint:errcheck
		}()

		for {
			select {
			case <-ctx.Done():
				return nil
			case r := <-ch:
				if r.err != nil {
					return r.err
				}
				if !yield(r.val) {
					return nil
				}
			}
		}
	})
}

// Publish returns a sink function that publishes each item to an MQTT topic.
// marshal converts the item into a byte payload. qos specifies the MQTT QoS
// level (0, 1, or 2). Use with [kitsune.Pipeline.ForEach].
//
// The client is not disconnected when the pipeline ends — the caller owns it.
func Publish[T any](client mqtt.Client, topic string, qos byte, marshal func(T) ([]byte, error)) func(context.Context, T) error {
	return func(_ context.Context, item T) error {
		data, err := marshal(item)
		if err != nil {
			return err
		}
		token := client.Publish(topic, qos, false, data)
		token.Wait()
		return token.Error()
	}
}
