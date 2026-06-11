// Package sqs publishes snapshotter artifact events to an Amazon SQS queue.
package sqs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/zourzouvillys/laredo/snapshotter"
)

// Sink publishes artifact events to an SQS queue as JSON.
type Sink struct {
	client   *awssqs.Client
	queueURL string
}

// New returns an SQS event sink.
func New(client *awssqs.Client, queueURL string) *Sink {
	return &Sink{client: client, queueURL: queueURL}
}

var _ snapshotter.EventSink = (*Sink)(nil)

// Name implements snapshotter.EventSink.
func (s *Sink) Name() string { return "sqs:" + s.queueURL }

// Publish implements snapshotter.EventSink.
func (s *Sink) Publish(ctx context.Context, ev snapshotter.ArtifactEvent) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("sqs: marshal event: %w", err)
	}
	if _, err := s.client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:    aws.String(s.queueURL),
		MessageBody: aws.String(string(body)),
	}); err != nil {
		return fmt.Errorf("sqs: send: %w", err)
	}
	return nil
}
