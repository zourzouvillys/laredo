// Package sns publishes snapshotter artifact events to an Amazon SNS topic.
package sns

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssns "github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/zourzouvillys/laredo/snapshotter"
)

// Sink publishes artifact events to an SNS topic as JSON.
type Sink struct {
	client   *awssns.Client
	topicARN string
}

// New returns an SNS event sink.
func New(client *awssns.Client, topicARN string) *Sink {
	return &Sink{client: client, topicARN: topicARN}
}

var _ snapshotter.EventSink = (*Sink)(nil)

// Name implements snapshotter.EventSink.
func (s *Sink) Name() string { return "sns:" + s.topicARN }

// Publish implements snapshotter.EventSink.
func (s *Sink) Publish(ctx context.Context, ev snapshotter.ArtifactEvent) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("sns: marshal event: %w", err)
	}
	if _, err := s.client.Publish(ctx, &awssns.PublishInput{
		TopicArn: aws.String(s.topicARN),
		Message:  aws.String(string(body)),
	}); err != nil {
		return fmt.Errorf("sns: publish: %w", err)
	}
	return nil
}
