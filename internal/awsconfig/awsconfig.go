package awsconfig

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
)

var (
	cached aws.Config
	once   sync.Once
	err    error
)

// Load returns a shared AWS config, initializing it on first call.
func Load(ctx context.Context) (aws.Config, error) {
	once.Do(func() {
		cached, err = config.LoadDefaultConfig(ctx, config.WithRegion("us-west-2"))
		if err != nil {
			err = fmt.Errorf("failed to load AWS config: %w", err)
		}
	})
	return cached, err
}
