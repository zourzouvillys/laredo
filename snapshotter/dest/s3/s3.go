// Package s3 implements the snapshotter Destination interface against Amazon S3.
// The manifest uses S3 conditional writes (If-None-Match / If-Match on the ETag)
// for true compare-and-swap.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/zourzouvillys/laredo/snapshotter"
)

// Destination writes artifacts and the manifest to an S3 bucket under a prefix.
type Destination struct {
	client *awss3.Client
	bucket string
	prefix string
}

// New returns an S3 destination. prefix is prepended to every key (include a
// trailing slash if you want a folder, e.g. "config_document/").
func New(client *awss3.Client, bucket, prefix string) *Destination {
	return &Destination{client: client, bucket: bucket, prefix: prefix}
}

var _ snapshotter.Destination = (*Destination)(nil)

// Name implements snapshotter.Destination.
func (d *Destination) Name() string { return "s3://" + d.bucket + "/" + d.prefix }

func (d *Destination) fullKey(key string) string { return d.prefix + key }

// Put implements snapshotter.Destination.
func (d *Destination) Put(ctx context.Context, key string, body io.Reader) (string, int64, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return "", 0, fmt.Errorf("s3: read body: %w", err)
	}
	full := d.fullKey(key)
	if _, err := d.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(full),
		Body:   bytes.NewReader(data),
	}); err != nil {
		return "", 0, fmt.Errorf("s3: put %s: %w", full, err)
	}
	return "s3://" + d.bucket + "/" + full, int64(len(data)), nil
}

// Get implements snapshotter.Destination. The revision token is the object ETag.
func (d *Destination) Get(ctx context.Context, key string) ([]byte, string, bool, error) {
	full := d.fullKey(key)
	out, err := d.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(full),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, "", false, nil
		}
		return nil, "", false, fmt.Errorf("s3: get %s: %w", full, err)
	}
	defer func() { _ = out.Body.Close() }()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, "", false, fmt.Errorf("s3: read %s: %w", full, err)
	}
	return data, aws.ToString(out.ETag), true, nil
}

// PutCAS implements compare-and-swap via S3 conditional writes: If-None-Match:*
// to create, or If-Match:<etag> to update. A 412 PreconditionFailed maps to
// ErrCASConflict.
func (d *Destination) PutCAS(ctx context.Context, key string, body []byte, expectedRev string) (string, error) {
	full := d.fullKey(key)
	in := &awss3.PutObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(full),
		Body:   bytes.NewReader(body),
	}
	if expectedRev == "" {
		in.IfNoneMatch = aws.String("*")
	} else {
		in.IfMatch = aws.String(expectedRev)
	}
	out, err := d.client.PutObject(ctx, in)
	if err != nil {
		if isPreconditionFailed(err) {
			return "", snapshotter.ErrCASConflict
		}
		return "", fmt.Errorf("s3: putcas %s: %w", full, err)
	}
	return aws.ToString(out.ETag), nil
}

func isNotFound(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		code := ae.ErrorCode()
		return code == "NoSuchKey" || code == "NotFound"
	}
	return false
}

func isPreconditionFailed(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return strings.EqualFold(ae.ErrorCode(), "PreconditionFailed")
	}
	return false
}
