// Package queue provides durable asynchronous transport adapters.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqsTypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

var ErrSQSNotConfigured = errors.New("SQS queue URL is required")

type Event struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   string          `json:"aggregate_id"`
	Payload       json.RawMessage `json:"payload"`
}

type Publisher interface {
	Publish(context.Context, Event) error
}

type SQSConfig struct {
	Region   string
	QueueURL string
	Endpoint string // Local development emulator only.
}

type SQS struct {
	client   *sqs.Client
	queueURL string
}

type ReceivedEvent struct {
	ReceiptHandle string
	Event         Event
}

func NewSQS(ctx context.Context, cfg SQSConfig) (*SQS, error) {
	if strings.TrimSpace(cfg.QueueURL) == "" {
		return nil, ErrSQSNotConfigured
	}
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if strings.TrimSpace(cfg.Endpoint) != "" {
		endpoint := cfg.Endpoint
		options = append(options, awsconfig.WithBaseEndpoint(endpoint))
		// LocalStack validates signed requests but does not need real AWS
		// credentials. Avoid probing EC2 metadata during local development.
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("localstack", "localstack", "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, err
	}
	return &SQS{client: sqs.NewFromConfig(awsCfg), queueURL: cfg.QueueURL}, nil
}

func (s *SQS) Publish(ctx context.Context, event Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = s.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(s.queueURL),
		MessageBody: aws.String(string(body)),
		MessageAttributes: map[string]sqsTypes.MessageAttributeValue{
			"event_type": {DataType: aws.String("String"), StringValue: aws.String(event.Type)},
		},
	})
	return err
}

func (s *SQS) Receive(ctx context.Context, maxMessages int32) ([]ReceivedEvent, error) {
	result, err := s.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(s.queueURL),
		MaxNumberOfMessages: maxMessages,
		WaitTimeSeconds:     20,
		VisibilityTimeout:   60,
	})
	if err != nil {
		return nil, err
	}
	events := make([]ReceivedEvent, 0, len(result.Messages))
	for _, message := range result.Messages {
		if message.Body == nil || message.ReceiptHandle == nil {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(*message.Body), &event); err != nil {
			return nil, err
		}
		events = append(events, ReceivedEvent{ReceiptHandle: *message.ReceiptHandle, Event: event})
	}
	return events, nil
}

func (s *SQS) Delete(ctx context.Context, receiptHandle string) error {
	_, err := s.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(s.queueURL), ReceiptHandle: aws.String(receiptHandle)})
	return err
}
