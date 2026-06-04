//go:build integration

package s3_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	s3dest "github.com/zourzouvillys/laredo/snapshotter/dest/s3"
	"github.com/zourzouvillys/laredo/snapshotter/desttest"
)

// TestS3_Conformance runs the Destination conformance suite against a real S3
// endpoint. Point it at LocalStack (or AWS) via env:
//
//	LAREDO_TEST_S3_ENDPOINT  (e.g. http://localhost:4566; empty = real AWS)
//	LAREDO_TEST_S3_BUCKET    (required; must already exist)
//	AWS_REGION / AWS_*       (credentials via the standard chain)
//
// Skipped unless LAREDO_TEST_S3_BUCKET is set. Conditional writes require an S3
// implementation that supports If-Match/If-None-Match.
func TestS3_Conformance(t *testing.T) {
	bucket := os.Getenv("LAREDO_TEST_S3_BUCKET")
	if bucket == "" {
		t.Skip("set LAREDO_TEST_S3_BUCKET to run the S3 conformance test")
	}
	ctx := context.Background()

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	endpoint := os.Getenv("LAREDO_TEST_S3_ENDPOINT")
	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	})

	// Unique prefix per run so the conformance suite addresses an empty namespace.
	prefix := fmt.Sprintf("conformance/%d/", time.Now().UnixNano())
	desttest.Run(t, s3dest.New(client, bucket, prefix))
}
