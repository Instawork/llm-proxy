package notify

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSESClient struct {
	inputs []*sesv2.SendEmailInput
	err    error
	// failAt, if >= 0, fails only the call at this zero-based index instead
	// of every call.
	failAt int
}

func (f *fakeSESClient) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	idx := len(f.inputs)
	f.inputs = append(f.inputs, in)
	if f.err != nil && (f.failAt < 0 || idx == f.failAt) {
		return nil, f.err
	}
	return &sesv2.SendEmailOutput{}, nil
}

func TestSES_Send_OnePerRecipient(t *testing.T) {
	t.Parallel()

	client := &fakeSESClient{}
	s := newSESWithClient(client, "proxy@example.com", "LLM Proxy")

	err := s.Send(context.Background(), Notification{
		To:    []string{"admin1@example.com", "admin2@example.com"},
		Title: "Test subject",
		Body:  "Test body",
	})
	require.NoError(t, err)

	require.Len(t, client.inputs, 2)
	assert.Equal(t, `"LLM Proxy" <proxy@example.com>`, *client.inputs[0].FromEmailAddress)
	assert.Equal(t, []string{"admin1@example.com"}, client.inputs[0].Destination.ToAddresses)
	assert.Equal(t, []string{"admin2@example.com"}, client.inputs[1].Destination.ToAddresses)
	assert.Equal(t, "Test subject", *client.inputs[0].Content.Simple.Subject.Data)
	assert.Equal(t, "Test body", *client.inputs[0].Content.Simple.Body.Text.Data)
}

func TestSES_Send_NoFromName(t *testing.T) {
	t.Parallel()

	client := &fakeSESClient{}
	s := newSESWithClient(client, "proxy@example.com", "")

	err := s.Send(context.Background(), Notification{To: []string{"user@example.com"}, Title: "s", Body: "t"})
	require.NoError(t, err)
	assert.Equal(t, "proxy@example.com", *client.inputs[0].FromEmailAddress)
}

func TestSES_Send_ErrorFromClient(t *testing.T) {
	t.Parallel()

	client := &fakeSESClient{err: errors.New("throttled"), failAt: -1}
	s := newSESWithClient(client, "proxy@example.com", "")

	err := s.Send(context.Background(), Notification{To: []string{"user@example.com"}, Title: "s", Body: "t"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "throttled")
}

func TestSES_Send_PartialFailure(t *testing.T) {
	t.Parallel()

	client := &fakeSESClient{err: errors.New("throttled"), failAt: 1}
	s := newSESWithClient(client, "proxy@example.com", "")

	err := s.Send(context.Background(), Notification{
		To:    []string{"admin1@example.com", "admin2@example.com"},
		Title: "s",
		Body:  "t",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin2@example.com")
	assert.Contains(t, err.Error(), "throttled")
	require.Len(t, client.inputs, 2, "both recipients must still be attempted despite the first failure")
}

func TestSES_Send_NoRecipients(t *testing.T) {
	t.Parallel()
	s := newSESWithClient(&fakeSESClient{}, "proxy@example.com", "")
	err := s.Send(context.Background(), Notification{Title: "s", Body: "t"})
	require.Error(t, err)
}
