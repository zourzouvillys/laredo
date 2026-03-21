//go:build integration

package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/localstack"
	"github.com/testcontainers/testcontainers-go/wait"
)

// LocalStackContainer holds a running LocalStack testcontainer with
// pre-configured AWS SDK clients for S3 and Kinesis.
type LocalStackContainer struct {
	Container *localstack.LocalStackContainer
	Endpoint  string
	S3Client  *s3.Client
	KinesisClient *kinesis.Client
}

// NewTestLocalStack starts a LocalStack testcontainer with S3 and Kinesis
// services enabled. The container is automatically terminated when the test finishes.
func NewTestLocalStack(t *testing.T) *LocalStackContainer {
	t.Helper()
	ctx := context.Background()

	lsContainer, err := localstack.Run(ctx,
		"localstack/localstack:4.4",
		testcontainers.WithEnv(map[string]string{
			"SERVICES": "s3,kinesis",
		}),
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready.").
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start localstack container: %v", err)
	}

	t.Cleanup(func() {
		if err := lsContainer.Terminate(ctx); err != nil {
			t.Logf("terminate localstack: %v", err)
		}
	})

	// Get the endpoint URL.
	host, err := lsContainer.Host(ctx)
	if err != nil {
		t.Fatalf("get localstack host: %v", err)
	}
	port, err := lsContainer.MappedPort(ctx, "4566/tcp")
	if err != nil {
		t.Fatalf("get localstack port: %v", err)
	}
	endpoint := "http://" + host + ":" + port.Port()

	// Create AWS config pointing at LocalStack.
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "test")),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}

	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	kinesisClient := kinesis.NewFromConfig(cfg, func(o *kinesis.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	return &LocalStackContainer{
		Container:     lsContainer,
		Endpoint:      endpoint,
		S3Client:      s3Client,
		KinesisClient: kinesisClient,
	}
}

// CreateS3Bucket creates an S3 bucket in LocalStack for testing.
func (ls *LocalStackContainer) CreateS3Bucket(t *testing.T, bucket string) {
	t.Helper()
	ctx := context.Background()

	_, err := ls.S3Client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("create s3 bucket %q: %v", bucket, err)
	}
}

// CreateKinesisStream creates a Kinesis stream in LocalStack for testing.
func (ls *LocalStackContainer) CreateKinesisStream(t *testing.T, streamName string, shardCount int) {
	t.Helper()
	ctx := context.Background()

	_, err := ls.KinesisClient.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(int32(shardCount)), //nolint:gosec // test code
	})
	if err != nil {
		t.Fatalf("create kinesis stream %q: %v", streamName, err)
	}

	// Wait for stream to become active.
	waiter := kinesis.NewStreamExistsWaiter(ls.KinesisClient)
	if err := waiter.Wait(ctx, &kinesis.DescribeStreamInput{
		StreamName: aws.String(streamName),
	}, 30*time.Second); err != nil {
		t.Fatalf("wait for kinesis stream %q: %v", streamName, err)
	}
}

// PutKinesisRecord puts a single record into a Kinesis stream.
func (ls *LocalStackContainer) PutKinesisRecord(t *testing.T, streamName, partitionKey string, data []byte) {
	t.Helper()
	ctx := context.Background()

	_, err := ls.KinesisClient.PutRecord(ctx, &kinesis.PutRecordInput{
		StreamName:   aws.String(streamName),
		PartitionKey: aws.String(partitionKey),
		Data:         data,
	})
	if err != nil {
		t.Fatalf("put kinesis record: %v", err)
	}
}
