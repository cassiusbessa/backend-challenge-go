// Package broker is how this process reaches the AWS messaging endpoints: the
// credential chain, the local endpoint override, and the FIFO topic the
// settlement publishes to.
//
// The credential chain lives here so the readiness probe of the queue and the
// publisher of the topic take the same one. Two copies of it would be two places
// to remember that IMDS has to stay off.
package broker

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
)

// LoadAWS takes the credential from the SDK default chain: the environment on
// Compose and on CI, the role in the cloud.
//
// IMDS is disabled so that the chain does not wait on an address that only
// answers inside EC2.
func LoadAWS(ctx context.Context) (aws.Config, error) {
	return awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithDefaultRegion("us-east-1"),
		awsconfig.WithEC2IMDSClientEnableState(imds.ClientDisabled),
	)
}
