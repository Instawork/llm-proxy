package notify

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// sesClient is the subset of *sesv2.Client the SES sender needs. Narrowing
// it to an interface keeps the AWS SDK out of tests.
type sesClient interface {
	SendEmail(ctx context.Context, in *sesv2.SendEmailInput, opts ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

// SES sends notifications as email via Amazon SES v2.
type SES struct {
	client sesClient
	from   string
}

// NewSES builds an SES sender, loading AWS credentials from the default
// provider chain. region may be empty to fall back to the environment's
// default region (AWS_REGION or the instance's configured region).
func NewSES(region, fromAddress, fromName string) (*SES, error) {
	startupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(startupCtx, opts...)
	if err != nil {
		return nil, fmt.Errorf("ses: load aws config: %w", err)
	}

	return newSESWithClient(sesv2.NewFromConfig(awsCfg), fromAddress, fromName), nil
}

// newSESWithClient builds an SES sender around an existing client. Used
// directly in tests with a fake client.
func newSESWithClient(client sesClient, fromAddress, fromName string) *SES {
	from := fromAddress
	if fromName != "" {
		from = (&mail.Address{Name: fromName, Address: fromAddress}).String()
	}
	return &SES{client: client, from: from}
}

// Send issues one SendEmail call per recipient, matching SendGrid's
// per-recipient personalization so recipients never see each other's
// addresses. Errors from individual recipients are joined.
func (s *SES) Send(ctx context.Context, n Notification) error {
	if len(n.To) == 0 {
		return fmt.Errorf("ses: notification has no recipients")
	}

	var errs []error
	for _, addr := range n.To {
		input := &sesv2.SendEmailInput{
			FromEmailAddress: &s.from,
			Destination:      &types.Destination{ToAddresses: []string{addr}},
			Content: &types.EmailContent{
				Simple: &types.Message{
					Subject: &types.Content{Data: &n.Title},
					Body: &types.Body{
						Text: &types.Content{Data: &n.Body},
					},
				},
			},
		}
		if _, err := s.client.SendEmail(ctx, input); err != nil {
			errs = append(errs, fmt.Errorf("ses: send to %s: %w", addr, err))
		}
	}
	return errors.Join(errs...)
}
