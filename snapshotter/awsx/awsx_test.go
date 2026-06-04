package awsx

import (
	"context"
	"testing"
)

func TestConfig_Ambient(t *testing.T) {
	cfg, err := Config(context.Background(), Profile{Type: "ambient", Region: "us-east-1"})
	if err != nil {
		t.Fatalf("ambient config: %v", err)
	}
	if cfg.Region != "us-east-1" {
		t.Fatalf("region = %q, want us-east-1", cfg.Region)
	}
}

func TestConfig_DefaultIsAmbient(t *testing.T) {
	if _, err := Config(context.Background(), Profile{Region: "eu-west-1"}); err != nil {
		t.Fatalf("empty type should default to ambient: %v", err)
	}
}

func TestConfig_AssumeRole(t *testing.T) {
	cfg, err := Config(context.Background(), Profile{
		Type:        "assume_role",
		Region:      "us-east-1",
		RoleARN:     "arn:aws:iam::123456789012:role/laredo",
		ExternalID:  "ext",
		SessionName: "sess",
	})
	if err != nil {
		t.Fatalf("assume_role config: %v", err)
	}
	// The credentials provider is layered (lazily resolved; no STS call here).
	if cfg.Credentials == nil {
		t.Fatal("assume_role should set a credentials provider")
	}
}

func TestConfig_Errors(t *testing.T) {
	if _, err := Config(context.Background(), Profile{Type: "assume_role"}); err == nil {
		t.Fatal("assume_role without role_arn should error")
	}
	if _, err := Config(context.Background(), Profile{Type: "bogus"}); err == nil {
		t.Fatal("unknown type should error")
	}
}
