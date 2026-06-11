package destwire

import (
	"context"
	"strings"
	"testing"

	localdest "github.com/zourzouvillys/laredo/snapshotter/dest/local"
	s3dest "github.com/zourzouvillys/laredo/snapshotter/dest/s3"
)

func TestBuildDestination_Local(t *testing.T) {
	d, err := BuildDestination(context.Background(), DestinationSpec{Type: "local", Path: t.TempDir()}, nil)
	if err != nil {
		t.Fatalf("local: %v", err)
	}
	if _, ok := d.(*localdest.Destination); !ok {
		t.Errorf("expected *local.Destination, got %T", d)
	}
}

func TestBuildDestination_S3(t *testing.T) {
	// S3 construction is network-free: credentials resolve lazily, so no live
	// AWS is needed to build the destination.
	d, err := BuildDestination(context.Background(),
		DestinationSpec{Type: "s3", Bucket: "b", Prefix: "p/", Region: "us-east-1"},
		AmbientAWSConfig)
	if err != nil {
		t.Fatalf("s3: %v", err)
	}
	if _, ok := d.(*s3dest.Destination); !ok {
		t.Errorf("expected *s3.Destination, got %T", d)
	}
}

func TestBuildDestination_Errors(t *testing.T) {
	cases := []struct {
		name    string
		spec    DestinationSpec
		aws     AWSConfigFunc
		wantSub string
	}{
		{"empty type", DestinationSpec{}, nil, "type is required"},
		{"unknown type", DestinationSpec{Type: "gcs"}, nil, "unknown destination type"},
		{"local no path", DestinationSpec{Type: "local"}, nil, "requires a path"},
		{"s3 no bucket", DestinationSpec{Type: "s3"}, AmbientAWSConfig, "requires a bucket"},
		{"s3 no resolver", DestinationSpec{Type: "s3", Bucket: "b"}, nil, "requires an AWS config resolver"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := BuildDestination(context.Background(), c.spec, c.aws)
			if err == nil || !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("got %v, want substring %q", err, c.wantSub)
			}
		})
	}
}

func TestBuildFormats(t *testing.T) {
	fs, err := BuildFormats([]string{"jsonl", "protobuf"})
	if err != nil {
		t.Fatalf("BuildFormats: %v", err)
	}
	if len(fs) != 2 {
		t.Fatalf("expected 2 formats, got %d", len(fs))
	}
	if fs[0].FormatID() != "jsonl" || fs[1].FormatID() != "protobuf" {
		t.Errorf("unexpected format ids: %q, %q", fs[0].FormatID(), fs[1].FormatID())
	}
	if _, err := BuildFormats([]string{"xml"}); err == nil {
		t.Error("expected error for unknown format")
	}
}
