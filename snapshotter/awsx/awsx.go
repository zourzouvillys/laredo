// Package awsx resolves AWS credentials per action group, so one process can use
// different roles for different actions (e.g. write S3 under one role, publish to
// a cross-account Kinesis stream under another).
package awsx

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Profile describes how to resolve credentials for one action group.
type Profile struct {
	// Type is "ambient" (the SDK default chain) or "assume_role".
	Type string
	// Region overrides the region for clients built from this profile.
	Region string
	// RoleARN, ExternalID and SessionName apply when Type == "assume_role".
	RoleARN     string
	ExternalID  string
	SessionName string
}

// Config builds an aws.Config for the profile.
//
//   - "" / "ambient": the SDK default credential chain — environment variables,
//     web identity (IRSA), and EC2/ECS instance/task roles.
//   - "assume_role": layers stscreds.AssumeRoleProvider on top of the ambient
//     base identity, caching the temporary credentials.
func Config(ctx context.Context, p Profile) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error
	if p.Region != "" {
		opts = append(opts, config.WithRegion(p.Region))
	}
	base, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("awsx: load base config: %w", err)
	}

	switch p.Type {
	case "", "ambient":
		return base, nil
	case "assume_role":
		if p.RoleARN == "" {
			return aws.Config{}, fmt.Errorf("awsx: assume_role requires role_arn")
		}
		stsClient := sts.NewFromConfig(base)
		provider := stscreds.NewAssumeRoleProvider(stsClient, p.RoleARN, func(o *stscreds.AssumeRoleOptions) {
			if p.ExternalID != "" {
				o.ExternalID = &p.ExternalID
			}
			if p.SessionName != "" {
				o.RoleSessionName = p.SessionName
			}
		})
		base.Credentials = aws.NewCredentialsCache(provider)
		return base, nil
	default:
		return aws.Config{}, fmt.Errorf("awsx: unknown credential type %q", p.Type)
	}
}
