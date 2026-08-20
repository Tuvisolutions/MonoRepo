package email

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/platform/config"
)

const ProviderDisabled = "disabled"

var (
	ErrSendingDisabled    = errors.New("email sending is disabled")
	ErrAccountUnavailable = errors.New("email account failed before message acceptance")
	ErrRetryableRejection = errors.New("email failed before provider acceptance; retry later")
)

const (
	AccountUnavailableErrorCode      = "credential_or_authorization_rejected"
	GmailRateLimitRejectedErrorCode  = "gmail_rate_limit_rejected"
	GmailPreSendUnavailableErrorCode = "gmail_pre_send_unavailable"
)

// RetryableRejectionError records a provider-confirmed pre-acceptance failure,
// including failure to acquire the provider access token before the message
// endpoint is called. Its code is intentionally controlled by this package so
// provider response text cannot leak into the delivery ledger.
type RetryableRejectionError struct {
	code  string
	cause error
}

func (err *RetryableRejectionError) Error() string {
	if err == nil || err.cause == nil {
		return ErrRetryableRejection.Error()
	}
	return ErrRetryableRejection.Error() + ": " + err.cause.Error()
}

func (err *RetryableRejectionError) Is(target error) bool {
	return target == ErrRetryableRejection
}

func (err *RetryableRejectionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// RetryableRejectionErrorCode returns the stable, sanitized provider code for
// a confirmed retryable rejection. It returns an empty string for all other
// errors, including ambiguous provider outcomes.
func RetryableRejectionErrorCode(err error) string {
	var rejected *RetryableRejectionError
	if !errors.As(err, &rejected) {
		return ""
	}
	return rejected.code
}

func newRetryableRejectionError(code string, cause error) error {
	return &RetryableRejectionError{code: strings.TrimSpace(code), cause: cause}
}

type SendRequest struct {
	To         string
	FromEmail  string
	Subject    string
	HTMLBody   string
	TextBody   string
	Signature  *SignatureDetails
	ReplyTo    string
	ThreadID   string
	InReplyTo  string
	References string
	Metadata   map[string]string
	Delivery   *DeliveryContext
}

type SendResult struct {
	ProviderMessageID string
	ProviderThreadID  string
	RFCMessageID      string
	FromEmail         string
	ReplyTo           string
	RedirectedTo      string
	Skipped           bool
	QuotaManaged      bool
	Finalized         bool
	DeliveryAttemptID uuid.UUID
	SendSequence      int64
	AccountKey        string
	AccountCycle      int64
	AccountSequence   int
}

type Provider interface {
	Send(ctx context.Context, req SendRequest) (SendResult, error)
}

func NewFromConfig(emailCfg config.EmailConfig, zohoCfg config.ZohoMailConfig) (Provider, error) {
	provider := strings.ToLower(strings.TrimSpace(emailCfg.Provider))
	if provider == "" || provider == ProviderDisabled || emailCfg.DisableSending {
		return NewDisabled(), nil
	}

	switch provider {
	case ProviderResend, "http", "https":
		return NewResend(emailCfg)
	case "zoho":
		return NewZoho(emailCfg, zohoCfg)
	case "smtp":
		return nil, fmt.Errorf("email provider smtp is no longer supported; use EMAIL_PROVIDER=resend or zoho")
	default:
		return nil, fmt.Errorf("unsupported email provider %q (supported: resend, zoho, http, https, disabled)", emailCfg.Provider)
	}
}
