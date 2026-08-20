package email_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/platform/config"
	emailprovider "github.com/rajchodisetti/restaurant-platform/backend/internal/providers/email"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestGmailProviderClassifiesPreAcceptanceFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		tokenStatus            int
		tokenBody              string
		sendStatus             int
		sendBody               string
		wantAccountUnavailable bool
		wantRetryable          bool
		wantErrorCode          string
		wantSends              int
		forbidErrorText        string
	}{
		{
			name: "revoked refresh token", tokenStatus: http.StatusBadRequest,
			tokenBody:              `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`,
			wantAccountUnavailable: true,
		},
		{
			name: "missing Gmail send permission", tokenStatus: http.StatusOK,
			tokenBody:              `{"access_token":"access-token","expires_in":3600}`,
			sendStatus:             http.StatusForbidden,
			sendBody:               `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Insufficient Permission","errors":[{"domain":"global","reason":"insufficientPermissions"}]}}`,
			wantAccountUnavailable: true, wantSends: 1,
		},
		{
			name: "authorization rejection without JSON", tokenStatus: http.StatusOK,
			tokenBody:              `{"access_token":"access-token","expires_in":3600}`,
			sendStatus:             http.StatusUnauthorized,
			sendBody:               "",
			wantAccountUnavailable: true, wantSends: 1,
		},
		{
			name: "per-user rate limit", tokenStatus: http.StatusOK,
			tokenBody:     `{"access_token":"access-token","expires_in":3600}`,
			sendStatus:    http.StatusForbidden,
			sendBody:      `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Rate limit for owner@example.com","errors":[{"domain":"usageLimits","reason":"userRateLimitExceeded"}]}}`,
			wantRetryable: true, wantErrorCode: emailprovider.GmailRateLimitRejectedErrorCode,
			wantSends: 1, forbidErrorText: "owner@example.com",
		},
		{
			name: "application request rate limit", tokenStatus: http.StatusOK,
			tokenBody:     `{"access_token":"access-token","expires_in":3600}`,
			sendStatus:    http.StatusForbidden,
			sendBody:      `{"error":{"code":403,"message":"Rate Limit Exceeded","errors":[{"domain":"global","reason":"backendError"},{"domain":"usageLimits","reason":"rateLimitExceeded"}]}}`,
			wantRetryable: true, wantErrorCode: emailprovider.GmailRateLimitRejectedErrorCode, wantSends: 1,
		},
		{
			name: "project daily rate limit", tokenStatus: http.StatusOK,
			tokenBody:     `{"access_token":"access-token","expires_in":3600}`,
			sendStatus:    http.StatusForbidden,
			sendBody:      `{"error":{"code":403,"message":"Daily Limit Exceeded","errors":[{"domain":"usageLimits","reason":"dailyLimitExceeded"}]}}`,
			wantRetryable: true, wantErrorCode: emailprovider.GmailRateLimitRejectedErrorCode, wantSends: 1,
		},
		{
			name: "mail sending limit with malformed response", tokenStatus: http.StatusOK,
			tokenBody:     `{"access_token":"access-token","expires_in":3600}`,
			sendStatus:    http.StatusTooManyRequests,
			sendBody:      `<html>Too many requests</html>`,
			wantRetryable: true, wantErrorCode: emailprovider.GmailRateLimitRejectedErrorCode, wantSends: 1,
		},
		{
			name: "unverified 403 remains an account rejection", tokenStatus: http.StatusOK,
			tokenBody:              `{"access_token":"access-token","expires_in":3600}`,
			sendStatus:             http.StatusForbidden,
			sendBody:               `{"error":{"code":403,"status":"RESOURCE_EXHAUSTED","message":"Unclassified rejection"}}`,
			wantAccountUnavailable: true, wantSends: 1,
		},
		{
			name: "temporary token endpoint failure is retryable before send", tokenStatus: http.StatusInternalServerError,
			tokenBody:     `{"error":"temporarily_unavailable"}`,
			wantRetryable: true, wantErrorCode: emailprovider.GmailPreSendUnavailableErrorCode,
		},
		{
			name: "token endpoint rate limit is retryable before send", tokenStatus: http.StatusTooManyRequests,
			tokenBody:     `<html>Too many requests</html>`,
			wantRetryable: true, wantErrorCode: emailprovider.GmailPreSendUnavailableErrorCode,
		},
		{
			name: "malformed token success is retryable before send", tokenStatus: http.StatusOK,
			tokenBody:     `<html>not JSON</html>`,
			wantRetryable: true, wantErrorCode: emailprovider.GmailPreSendUnavailableErrorCode,
		},
		{
			name: "invalid token request is an account configuration rejection", tokenStatus: http.StatusBadRequest,
			tokenBody:              `{"error":"invalid_request"}`,
			wantAccountUnavailable: true,
		},
		{
			name: "ambiguous provider failure", tokenStatus: http.StatusOK,
			tokenBody:  `{"access_token":"access-token","expires_in":3600}`,
			sendStatus: http.StatusInternalServerError,
			sendBody:   `{"error":{"code":500,"status":"INTERNAL","message":"Internal error"}}`,
			wantSends:  1,
		},
		{
			name: "malformed accepted response is ambiguous", tokenStatus: http.StatusOK,
			tokenBody:  `{"access_token":"access-token","expires_in":3600}`,
			sendStatus: http.StatusOK,
			sendBody:   `<html>not JSON</html>`,
			wantSends:  1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sendRequests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/token":
					w.WriteHeader(test.tokenStatus)
					_, _ = w.Write([]byte(test.tokenBody))
				case "/gmail/v1/users/me/messages/send":
					sendRequests++
					w.WriteHeader(test.sendStatus)
					_, _ = w.Write([]byte(test.sendBody))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			provider, err := emailprovider.NewGmailWithClient(
				config.EmailConfig{},
				config.GmailMailConfig{
					MailboxEmail: "sales@example.com", ClientID: "client-id",
					ClientSecret: "client-secret", RefreshToken: "refresh-token",
				},
				server.Client(), server.URL+"/gmail/v1", server.URL+"/token",
			)
			if err != nil {
				t.Fatalf("NewGmailWithClient() error = %v", err)
			}

			_, err = provider.Send(context.Background(), emailprovider.SendRequest{
				To: "owner@example.com", Subject: "Test", TextBody: "Body",
			})
			if err == nil {
				t.Fatal("Send() error = nil, want provider failure")
			}
			if got := errors.Is(err, emailprovider.ErrAccountUnavailable); got != test.wantAccountUnavailable {
				t.Fatalf("errors.Is(ErrAccountUnavailable) = %v, want %v; error = %v", got, test.wantAccountUnavailable, err)
			}
			if got := errors.Is(err, emailprovider.ErrRetryableRejection); got != test.wantRetryable {
				t.Fatalf("errors.Is(ErrRetryableRejection) = %v, want %v; error = %v", got, test.wantRetryable, err)
			}
			if got := emailprovider.RetryableRejectionErrorCode(err); got != test.wantErrorCode {
				t.Fatalf("RetryableRejectionErrorCode() = %q, want %q; error = %v", got, test.wantErrorCode, err)
			}
			if test.wantRetryable {
				var typed *emailprovider.RetryableRejectionError
				if !errors.As(err, &typed) {
					t.Fatalf("errors.As(*RetryableRejectionError) = false; error = %v", err)
				}
			}
			if test.forbidErrorText != "" && strings.Contains(err.Error(), test.forbidErrorText) {
				t.Fatalf("Send() error leaked sanitized provider text %q: %v", test.forbidErrorText, err)
			}
			if sendRequests != test.wantSends {
				t.Fatalf("Gmail send requests = %d, want %d", sendRequests, test.wantSends)
			}
		})
	}
}

func TestGmailProviderLeavesTransportFailureAmbiguous(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/token" {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     http.StatusText(http.StatusOK),
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"access-token","expires_in":3600}`)),
				Request:    request,
			}, nil
		}
		return nil, errors.New("connection reset")
	})}
	provider, err := emailprovider.NewGmailWithClient(
		config.EmailConfig{},
		config.GmailMailConfig{
			MailboxEmail: "sales@example.com", ClientID: "client-id",
			ClientSecret: "client-secret", RefreshToken: "refresh-token",
		},
		client, "http://gmail.test/gmail/v1", "http://gmail.test/token",
	)
	if err != nil {
		t.Fatalf("NewGmailWithClient() error = %v", err)
	}

	_, err = provider.Send(context.Background(), emailprovider.SendRequest{
		To: "owner@example.com", Subject: "Test", TextBody: "Body",
	})
	if err == nil {
		t.Fatal("Send() error = nil, want transport failure")
	}
	if errors.Is(err, emailprovider.ErrAccountUnavailable) || errors.Is(err, emailprovider.ErrRetryableRejection) {
		t.Fatalf("Send() error was classified as a confirmed pre-acceptance rejection: %v", err)
	}
	if got := emailprovider.RetryableRejectionErrorCode(err); got != "" {
		t.Fatalf("RetryableRejectionErrorCode() = %q, want empty code", got)
	}
}

func TestGmailProviderClassifiesTokenTransportFailureBeforeSend(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/token" {
			t.Fatalf("unexpected request path %q; Gmail send endpoint must not be called", request.URL.Path)
		}
		return nil, errors.New("token connection reset")
	})}
	provider, err := emailprovider.NewGmailWithClient(
		config.EmailConfig{},
		config.GmailMailConfig{
			MailboxEmail: "sales@example.com", ClientID: "client-id",
			ClientSecret: "client-secret", RefreshToken: "refresh-token",
		},
		client, "http://gmail.test/gmail/v1", "http://gmail.test/token",
	)
	if err != nil {
		t.Fatalf("NewGmailWithClient() error = %v", err)
	}

	_, err = provider.Send(context.Background(), emailprovider.SendRequest{
		To: "owner@example.com", Subject: "Test", TextBody: "Body",
	})
	if !errors.Is(err, emailprovider.ErrRetryableRejection) {
		t.Fatalf("Send() error = %v, want ErrRetryableRejection", err)
	}
	if got := emailprovider.RetryableRejectionErrorCode(err); got != emailprovider.GmailPreSendUnavailableErrorCode {
		t.Fatalf("RetryableRejectionErrorCode() = %q, want %q", got, emailprovider.GmailPreSendUnavailableErrorCode)
	}
}

func TestGmailProviderClassifiesCancelledPreSendContext(t *testing.T) {
	t.Parallel()

	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("must not be called")
	})}
	provider, err := emailprovider.NewGmailWithClient(
		config.EmailConfig{},
		config.GmailMailConfig{
			MailboxEmail: "sales@example.com", ClientID: "client-id",
			ClientSecret: "client-secret", RefreshToken: "refresh-token",
		},
		client, "http://gmail.test/gmail/v1", "http://gmail.test/token",
	)
	if err != nil {
		t.Fatalf("NewGmailWithClient() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = provider.Send(ctx, emailprovider.SendRequest{
		To: "owner@example.com", Subject: "Test", TextBody: "Body",
	})
	if !errors.Is(err, emailprovider.ErrRetryableRejection) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Send() error = %v, want retryable pre-send context cancellation", err)
	}
	if got := emailprovider.RetryableRejectionErrorCode(err); got != emailprovider.GmailPreSendUnavailableErrorCode {
		t.Fatalf("RetryableRejectionErrorCode() = %q, want %q", got, emailprovider.GmailPreSendUnavailableErrorCode)
	}
	if requests != 0 {
		t.Fatalf("HTTP requests = %d, want zero", requests)
	}
}

func TestGmailProviderSendsViaHTTPSAPIContract(t *testing.T) {
	var tokenForm url.Values
	var rawMessage string
	var requestThreadID string
	var tokenRequests int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenRequests++
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			tokenForm = r.Form
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"access-token","token_type":"Bearer","expires_in":3600}`))
		case "/gmail/v1/users/me/messages/send":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %q, want POST", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer access-token" {
				t.Fatalf("Authorization header was not set from refreshed token")
			}
			payload, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read send body: %v", err)
			}
			var body struct {
				Raw      string `json:"raw"`
				ThreadID string `json:"threadId"`
			}
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatalf("decode send body: %v", err)
			}
			decoded, err := base64.RawURLEncoding.DecodeString(body.Raw)
			if err != nil {
				t.Fatalf("decode raw MIME: %v", err)
			}
			rawMessage = string(decoded)
			requestThreadID = body.ThreadID
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"gmail-message-id","threadId":"gmail-thread-id"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider, err := emailprovider.NewGmailWithClient(
		config.EmailConfig{FromName: "Tuvi Solutions"},
		config.GmailMailConfig{
			MailboxEmail: "sales1@example.com",
			FromEmail:    "alias@example.com",
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			RefreshToken: "refresh-token",
		},
		server.Client(),
		server.URL+"/gmail/v1",
		server.URL+"/token",
	)
	if err != nil {
		t.Fatalf("NewGmailWithClient() error = %v", err)
	}

	result, err := provider.Send(context.Background(), emailprovider.SendRequest{
		To:         "owner@restaurant.example",
		FromEmail:  "sales1+reply-token@example.com",
		Subject:    "Your restaurant demo",
		TextBody:   "Text version",
		HTMLBody:   "<p>HTML version</p>",
		ReplyTo:    "contact@example.com",
		ThreadID:   "original-thread-id",
		InReplyTo:  "<original@example.com>",
		References: "<earlier@example.com> <original@example.com>",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if result.ProviderMessageID != "gmail-message-id" {
		t.Fatalf("ProviderMessageID = %q, want gmail-message-id", result.ProviderMessageID)
	}
	if result.ProviderThreadID != "gmail-thread-id" {
		t.Fatalf("ProviderThreadID = %q, want gmail-thread-id", result.ProviderThreadID)
	}
	if result.FromEmail != "sales1@example.com" {
		t.Fatalf("FromEmail = %q, want receiving mailbox", result.FromEmail)
	}
	if requestThreadID != "original-thread-id" {
		t.Fatalf("request threadId = %q, want original-thread-id", requestThreadID)
	}
	if result.RFCMessageID == "" || !strings.Contains(result.RFCMessageID, "@example.com") {
		t.Fatalf("RFCMessageID = %q, want a Message-ID using the from domain", result.RFCMessageID)
	}
	firstRawMessage := rawMessage
	if _, err := provider.Send(context.Background(), emailprovider.SendRequest{
		To:       "owner2@restaurant.example",
		Subject:  "Your second restaurant demo",
		TextBody: "Text version",
	}); err != nil {
		t.Fatalf("second Send() error = %v", err)
	}
	if tokenRequests != 1 {
		t.Fatalf("token requests = %d, want 1 cached refresh", tokenRequests)
	}
	if tokenForm.Get("grant_type") != "refresh_token" || tokenForm.Get("client_id") != "client-id" || tokenForm.Get("refresh_token") != "refresh-token" {
		t.Fatalf("token refresh form is incomplete")
	}
	for _, expected := range []string{
		"From: \"Tuvi Solutions\" <sales1@example.com>",
		"To: <owner@restaurant.example>",
		"Reply-To: <contact@example.com>",
		"In-Reply-To: <original@example.com>",
		"References: <earlier@example.com> <original@example.com>",
		"Message-ID:",
		"MIME-Version: 1.0",
		"multipart/alternative",
	} {
		if !strings.Contains(firstRawMessage, expected) {
			t.Fatalf("raw MIME message missing %q", expected)
		}
	}
}

func TestGmailProviderDoesNotForwardCredentialsAcrossRedirect(t *testing.T) {
	redirectRequests := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectRequests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, receiver.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	provider, err := emailprovider.NewGmailWithClient(
		config.EmailConfig{},
		config.GmailMailConfig{
			MailboxEmail: "sales1@example.com",
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			RefreshToken: "refresh-token",
		},
		redirector.Client(),
		redirector.URL+"/gmail/v1",
		redirector.URL+"/token",
	)
	if err != nil {
		t.Fatalf("NewGmailWithClient() error = %v", err)
	}

	_, err = provider.Send(context.Background(), emailprovider.SendRequest{
		To:       "owner@restaurant.example",
		Subject:  "Your restaurant demo",
		TextBody: "hello",
	})
	if err == nil {
		t.Fatal("Send() error = nil, want redirect rejection")
	}
	if redirectRequests != 0 {
		t.Fatalf("redirect target requests = %d, want 0", redirectRequests)
	}
}

func TestGmailProviderRejectsHeaderInjection(t *testing.T) {
	provider, err := emailprovider.NewGmailWithClient(
		config.EmailConfig{},
		config.GmailMailConfig{
			MailboxEmail: "sales1@example.com",
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			RefreshToken: "refresh-token",
		},
		http.DefaultClient,
		"http://localhost/gmail/v1",
		"http://localhost/token",
	)
	if err != nil {
		t.Fatalf("NewGmailWithClient() error = %v", err)
	}

	_, err = provider.Send(context.Background(), emailprovider.SendRequest{
		To:       "owner@restaurant.example",
		Subject:  "safe\r\nBcc: attacker@example.com",
		TextBody: "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "newline") {
		t.Fatalf("Send() error = %v, want newline rejection", err)
	}

	_, err = provider.Send(context.Background(), emailprovider.SendRequest{
		To:        "owner@restaurant.example",
		FromEmail: "different@example.com",
		Subject:   "Safe subject",
		TextBody:  "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("Send() error = %v, want unauthorized from-address rejection", err)
	}
}

func TestGmailProviderRejectsInvalidStaticEmailConfig(t *testing.T) {
	tests := []struct {
		name     string
		emailCfg config.EmailConfig
		want     string
	}{
		{name: "from name newline", emailCfg: config.EmailConfig{FromName: "Tuvi\r\nBcc: attacker@example.com"}, want: "newline"},
		{name: "invalid redirect", emailCfg: config.EmailConfig{RedirectTo: "not-an-email"}, want: "redirect recipient"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := emailprovider.NewGmailWithClient(
				test.emailCfg,
				config.GmailMailConfig{
					MailboxEmail: "sales1@example.com",
					ClientID:     "client-id",
					ClientSecret: "client-secret",
					RefreshToken: "refresh-token",
				},
				http.DefaultClient,
				"http://localhost/gmail/v1",
				"http://localhost/token",
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewGmailWithClient() error = %v, want %q", err, test.want)
			}
		})
	}
}
