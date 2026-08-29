package transport

import (
	"context"
	"fmt"
	"sync"
	"time"

	zmq "github.com/go-zeromq/zmq4"
)

const (
	TopicTrade       = "trade"
	TopicOrderStatus = "order_status"
	TopicOrderBook   = "order_book"
)

type RequestClient struct {
	socket zmq.Socket
	mu     sync.Mutex
}

func NewRequestClient(ctx context.Context, endpoint string, timeout time.Duration) (*RequestClient, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("ZeroMQ request endpoint is required")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("ZeroMQ request timeout must be positive")
	}
	socket := zmq.NewReq(
		ctx,
		zmq.WithTimeout(timeout),
		zmq.WithDialerTimeout(timeout),
		zmq.WithDialerRetry(50*time.Millisecond),
		zmq.WithAutomaticReconnect(true),
	)
	if err := socket.Dial(endpoint); err != nil {
		_ = socket.Close()
		return nil, fmt.Errorf("dial ZeroMQ request endpoint: %w", err)
	}
	return &RequestClient{socket: socket}, nil
}

func (client *RequestClient) Request(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("ZeroMQ request payload is required")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if err := client.socket.Send(zmq.NewMsg(payload)); err != nil {
		return nil, fmt.Errorf("send ZeroMQ request: %w", err)
	}
	reply, err := client.socket.Recv()
	if err != nil {
		return nil, fmt.Errorf("receive ZeroMQ reply: %w", err)
	}
	if len(reply.Frames) != 1 {
		return nil, fmt.Errorf("ZeroMQ reply must contain exactly one frame")
	}
	return append([]byte(nil), reply.Frames[0]...), nil
}

func (client *RequestClient) Close() error {
	return client.socket.Close()
}

type Event struct {
	Topic   string
	Payload []byte
}

type Subscriber struct {
	socket zmq.Socket
}

func NewSubscriber(ctx context.Context, endpoint string, timeout time.Duration, topics ...string) (*Subscriber, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("ZeroMQ subscriber endpoint is required")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("ZeroMQ subscriber timeout must be positive")
	}
	if len(topics) == 0 {
		return nil, fmt.Errorf("at least one ZeroMQ topic is required")
	}
	socket := zmq.NewSub(
		ctx,
		zmq.WithTimeout(timeout),
		zmq.WithDialerTimeout(timeout),
		zmq.WithDialerRetry(50*time.Millisecond),
		zmq.WithAutomaticReconnect(true),
	)
	for _, topic := range topics {
		if topic == "" {
			_ = socket.Close()
			return nil, fmt.Errorf("ZeroMQ topic cannot be empty")
		}
		if err := socket.SetOption(zmq.OptionSubscribe, topic); err != nil {
			_ = socket.Close()
			return nil, fmt.Errorf("subscribe to ZeroMQ topic %q: %w", topic, err)
		}
	}
	if err := socket.Dial(endpoint); err != nil {
		_ = socket.Close()
		return nil, fmt.Errorf("dial ZeroMQ subscriber endpoint: %w", err)
	}
	return &Subscriber{socket: socket}, nil
}

func (subscriber *Subscriber) Receive() (Event, error) {
	message, err := subscriber.socket.Recv()
	if err != nil {
		return Event{}, fmt.Errorf("receive ZeroMQ event: %w", err)
	}
	if len(message.Frames) != 2 {
		return Event{}, fmt.Errorf("ZeroMQ event must contain topic and payload frames")
	}
	if len(message.Frames[0]) == 0 || len(message.Frames[1]) == 0 {
		return Event{}, fmt.Errorf("ZeroMQ event topic and payload cannot be empty")
	}
	return Event{Topic: string(message.Frames[0]), Payload: append([]byte(nil), message.Frames[1]...)}, nil
}

func (subscriber *Subscriber) Close() error {
	return subscriber.socket.Close()
}
