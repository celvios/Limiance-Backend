//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/limiance/backend/internal/eventrouter"
	"github.com/limiance/backend/internal/notifications"
	"github.com/limiance/backend/internal/platform/queue"
	"github.com/limiance/backend/internal/security/envelope"
)

// TestOutboxRouterToNotificationsWorker exercises the real local SQS transport
// and the production SendGrid adapter against a controlled HTTP endpoint. It
// is opt-in because LocalStack is an external local dependency.
func TestOutboxRouterToNotificationsWorker(t *testing.T) {
	endpoint := os.Getenv("SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("SQS_ENDPOINT is required for LocalStack integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "eu-central-1"
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region), awsconfig.WithBaseEndpoint(endpoint))
	if err != nil {
		t.Fatal(err)
	}
	admin := sqs.NewFromConfig(awsCfg)
	prefix := fmt.Sprintf("limiance-router-test-%d", time.Now().UnixNano())
	urls := make([]string, 0, 3)
	for _, suffix := range []string{"outbox", "notifications", "unrouted"} {
		result, err := admin.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(prefix + "-" + suffix)})
		if err != nil {
			t.Fatal(err)
		}
		urls = append(urls, aws.ToString(result.QueueUrl))
	}
	defer func() {
		for _, url := range urls {
			_, _ = admin.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: aws.String(url)})
		}
	}()

	newQueue := func(url string) *queue.SQS {
		transport, err := queue.NewSQS(ctx, queue.SQSConfig{Region: region, QueueURL: url, Endpoint: endpoint})
		if err != nil {
			t.Fatal(err)
		}
		return transport
	}
	source, destination, unrouted := newQueue(urls[0]), newQueue(urls[1]), newQueue(urls[2])
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	codeCiphertext, err := envelope.Seal(key, "123456")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{
		"email":           "worker-test@limiance.test",
		"code_ciphertext": codeCiphertext,
		"expires_at":      time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Publish(ctx, queue.Event{ID: prefix, Type: "email.verification_requested", AggregateType: "user", AggregateID: prefix, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	router, err := eventrouter.New(source, map[string]queue.Publisher{"email.verification_requested": destination}, unrouted)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := router.RunOnce(ctx, 1); err != nil || count != 1 {
		t.Fatalf("router result: count=%d err=%v", count, err)
	}

	mailReceived := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/mail/send" {
			t.Errorf("unexpected SendGrid request %s %s", r.Method, r.URL.Path)
		}
		mailReceived <- struct{}{}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	mail, err := notifications.NewSendGrid(notifications.SendGridConfig{APIKey: "test-key", FromEmail: "security@limiance.test", TemplateID: "d-test", Endpoint: server.URL + "/v3/mail/send"})
	if err != nil {
		t.Fatal(err)
	}
	consumer := notifications.NewConsumer(destination, mail, key)
	if count, err := consumer.RunOnce(ctx); err != nil || count != 1 {
		t.Fatalf("notification worker result: count=%d err=%v", count, err)
	}
	select {
	case <-mailReceived:
	case <-ctx.Done():
		t.Fatal("SendGrid adapter was not invoked")
	}
}
