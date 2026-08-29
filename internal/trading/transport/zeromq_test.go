package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	zmq "github.com/go-zeromq/zmq4"
	"github.com/limiance/backend/internal/trading/protocol"
)

func TestRequestClientSendsOrderIngress(t *testing.T) {
	endpoint := availableEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	server := zmq.NewRep(ctx, zmq.WithTimeout(2*time.Second))
	defer server.Close()
	if err := server.Listen(endpoint); err != nil {
		t.Fatalf("listen: %v", err)
	}

	want := protocol.OrderIngress{
		ID: "order-1", UserID: "user-1", Pair: "BTCUSDT", Side: protocol.OrderSideBuy,
		OrderType: protocol.OrderTypeMarket, Quantity: 1000000, TimeInForce: protocol.TimeInForceIOC,
	}
	payload, err := protocol.EncodeOrderIngress(want)
	if err != nil {
		t.Fatalf("encode order: %v", err)
	}

	serverErr := make(chan error, 1)
	go func() {
		message, receiveErr := server.Recv()
		if receiveErr != nil {
			serverErr <- receiveErr
			return
		}
		if len(message.Frames) != 1 {
			serverErr <- fmt.Errorf("received %d frames", len(message.Frames))
			return
		}
		got, decodeErr := protocol.DecodeOrderIngress(message.Frames[0])
		if decodeErr != nil {
			serverErr <- decodeErr
			return
		}
		if got != want {
			serverErr <- fmt.Errorf("received order does not match sent order")
			return
		}
		serverErr <- server.Send(zmq.NewMsgString("accepted"))
	}()

	client, err := NewRequestClient(ctx, endpoint, 2*time.Second)
	if err != nil {
		t.Fatalf("new request client: %v", err)
	}
	defer client.Close()
	reply, err := client.Request(payload)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if string(reply) != "accepted" {
		t.Fatalf("unexpected reply %q", reply)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestSubscriberReconnectsAfterPublisherRestart(t *testing.T) {
	endpoint := availableEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	publisher := zmq.NewPub(ctx, zmq.WithTimeout(2*time.Second))
	if err := publisher.Listen(endpoint); err != nil {
		t.Fatalf("listen first publisher: %v", err)
	}
	subscriber, err := NewSubscriber(ctx, endpoint, 3*time.Second, TopicTrade)
	if err != nil {
		t.Fatalf("new subscriber: %v", err)
	}
	defer subscriber.Close()

	sendUntilCanceled(ctx, publisher, TopicTrade, []byte("before-restart"))
	event := receiveIgnoringEOF(t, subscriber)
	if string(event.Payload) != "before-restart" {
		t.Fatalf("unexpected first payload %q", event.Payload)
	}
	if err := publisher.Close(); err != nil {
		t.Fatalf("close first publisher: %v", err)
	}

	restarted := zmq.NewPub(ctx, zmq.WithTimeout(2*time.Second))
	defer restarted.Close()
	if err := restarted.Listen(endpoint); err != nil {
		t.Fatalf("listen restarted publisher: %v", err)
	}
	sendUntilCanceled(ctx, restarted, TopicTrade, []byte("after-restart"))
	for {
		event = receiveIgnoringEOF(t, subscriber)
		if string(event.Payload) == "after-restart" {
			break
		}
	}
}

func availableEndpoint(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve TCP port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release TCP port: %v", err)
	}
	return "tcp://" + address
}

func sendUntilCanceled(ctx context.Context, socket zmq.Socket, topic string, payload []byte) {
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = socket.SendMulti(zmq.NewMsgFrom([]byte(topic), payload))
			}
		}
	}()
}

func receiveIgnoringEOF(t *testing.T, subscriber *Subscriber) Event {
	t.Helper()
	for {
		event, err := subscriber.Receive()
		if errors.Is(err, io.EOF) {
			continue
		}
		if err != nil {
			t.Fatalf("receive event: %v", err)
		}
		return event
	}
}
