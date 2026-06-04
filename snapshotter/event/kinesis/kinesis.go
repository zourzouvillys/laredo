// Package kinesis publishes snapshotter artifact events to an Amazon Kinesis
// stream. The table name is used as the partition key so a table's events keep
// to one shard and stay ordered.
package kinesis

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awskinesis "github.com/aws/aws-sdk-go-v2/service/kinesis"

	"github.com/zourzouvillys/laredo/snapshotter"
)

// Sink publishes artifact events to a Kinesis stream as JSON records.
type Sink struct {
	client *awskinesis.Client
	stream string
}

// New returns a Kinesis event sink.
func New(client *awskinesis.Client, stream string) *Sink {
	return &Sink{client: client, stream: stream}
}

var _ snapshotter.EventSink = (*Sink)(nil)

// Name implements snapshotter.EventSink.
func (s *Sink) Name() string { return "kinesis:" + s.stream }

// Publish implements snapshotter.EventSink.
func (s *Sink) Publish(ctx context.Context, ev snapshotter.ArtifactEvent) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("kinesis: marshal event: %w", err)
	}
	partitionKey := ev.Table
	if partitionKey == "" {
		partitionKey = "laredo"
	}
	if _, err := s.client.PutRecord(ctx, &awskinesis.PutRecordInput{
		StreamName:   aws.String(s.stream),
		Data:         body,
		PartitionKey: aws.String(partitionKey),
	}); err != nil {
		return fmt.Errorf("kinesis: put record: %w", err)
	}
	return nil
}
