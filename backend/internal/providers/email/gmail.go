package email

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/platform/config"
)

const (
	gmailAPIBaseURL = "https://gmail.googleapis.com/gmail/v1"
	googleTokenURL  = "https://oauth2.googleapis.com/token"
)

type gmailProvider struct {
	cfg      config.GmailMailConfig
	email    config.EmailConfig
	client   *http.Client
	apiBase  string
	tokenURL string
	tokenMu  sync.Mutex
	token    string
	tokenExp time.Time
}

type gmailAPIErrorDetail struct {
	Reason string `json:"reason"`
}

func NewGmail(emailCfg config.EmailConfig, gmailCfg config.GmailMailConfig) (Provider, error) {
	return newGmailProvider(
		emailCfg,
		gmailCfg,
		&http.Client{Timeout: 30 * time.Second},
		gmailAPIBaseURL,
		googleTokenURL,
		true,
	)
}

// NewGmailWithClient supports isolated adapter tests without making production
// token or API endpoints configurable through the environment.
func NewGmailWithClient(
	emailCfg config.EmailConfig,
	gmailCfg config.GmailMailConfig,
	client *http.Client,
	apiBaseURL string,
	tokenURL string,
) (Provider, error) {
	return newGmailProvider(emailCfg, gmailCfg, client, apiBaseURL, tokenURL, false)
}

func newGmailProvider(
	emailCfg config.EmailConfig,
	gmailCfg config.GmailMailConfig,
	client *http.Client,
	apiBaseURL string,
	tokenURL string,
	requireGoogleEndpoints bool,
) (Provider, error) {
	if client == nil {
		return nil, fmt.Errorf("gmail provider requires an HTTP client")
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if _, err := cleanHeaderValue(emailCfg.FromName, "from name"); err != nil {
		return nil, err
	}
	if redirect := strings.TrimSpace(emailCfg.RedirectTo); redirect != "" {
		if _, err := canonicalMailbox(redirect); err != nil {
			return nil, fmt.Errorf("gmail redirect recipient: %w", err)
		}
	}
	mailboxEmail, err := canonicalMailbox(gmailCfg.MailboxEmail)
	if err != nil {
		return nil, fmt.Errorf("gmail mailbox_email: %w", err)
	}
	fromEmail := strings.TrimSpace(gmailCfg.FromEmail)
	if fromEmail == "" {
		fromEmail = mailboxEmail
	}
	fromEmail, err = canonicalMailbox(fromEmail)
	if err != nil {
		return nil, fmt.Errorf("gmail from_email: %w", err)
	}
	if strings.TrimSpace(gmailCfg.ClientID) == "" || strings.TrimSpace(gmailCfg.ClientSecret) == "" || strings.TrimSpace(gmailCfg.RefreshToken) == "" {
		return nil, fmt.Errorf("gmail client_id, client_secret, and refresh_token are required")
	}
	if err := validateGmailEndpoint(apiBaseURL, requireGoogleEndpoints, "gmail.googleapis.com"); err != nil {
		return nil, fmt.Errorf("gmail API endpoint: %w", err)
	}
	if err := validateGmailEndpoint(tokenURL, requireGoogleEndpoints, "oauth2.googleapis.com"); err != nil {
		return nil, fmt.Errorf("gmail OAuth endpoint: %w", err)
	}

	gmailCfg.MailboxEmail = mailboxEmail
	gmailCfg.FromEmail = fromEmail
	return &gmailProvider{
		cfg:      gmailCfg,
		email:    emailCfg,
		client:   &clientCopy,
		apiBase:  strings.TrimRight(apiBaseURL, "/"),
		tokenURL: tokenURL,
	}, nil
}

func (provider *gmailProvider) Send(ctx context.Context, req SendRequest) (SendResult, error) {
	if err := ctx.Err(); err != nil {
		return SendResult{}, newRetryableRejectionError(GmailPreSendUnavailableErrorCode, err)
	}
	req = EnsureTuviSignature(req)

	to, originalTo := resolveRecipient(provider.email, req.To)
	to, err := canonicalMailbox(to)
	if err != nil {
		return SendResult{}, fmt.Errorf("gmail recipient: %w", err)
	}
	fromEmail, err := resolveGmailFromEmail(req.FromEmail, provider.cfg.MailboxEmail, provider.cfg.FromEmail)
	if err != nil {
		return SendResult{}, err
	}

	rawMessage, rfcMessageID, err := buildGmailMessage(
		fromEmail,
		provider.email.FromName,
		to,
		req.ReplyTo,
		req.Subject,
		req.TextBody,
		req.HTMLBody,
		req.InReplyTo,
		req.References,
	)
	if err != nil {
		return SendResult{}, err
	}

	accessToken, err := provider.accessToken(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return SendResult{}, newRetryableRejectionError(GmailPreSendUnavailableErrorCode, ctx.Err())
		}
		if errors.Is(err, ErrAccountUnavailable) {
			return SendResult{}, err
		}
		// The Gmail message endpoint has not been called yet, so transient token
		// acquisition failures are proven non-delivery rather than ambiguous
		// provider outcomes.
		return SendResult{}, newRetryableRejectionError(GmailPreSendUnavailableErrorCode, err)
	}

	payloadFields := map[string]string{
		"raw": base64.RawURLEncoding.EncodeToString(rawMessage),
	}
	if threadID := strings.TrimSpace(req.ThreadID); threadID != "" {
		payloadFields["threadId"] = threadID
	}
	payload, err := json.Marshal(payloadFields)
	if err != nil {
		return SendResult{}, fmt.Errorf("gmail marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		provider.apiBase+"/users/me/messages/send",
		bytes.NewReader(payload),
	)
	if err != nil {
		return SendResult{}, fmt.Errorf("gmail build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := provider.client.Do(httpReq)
	if err != nil {
		return SendResult{}, fmt.Errorf("gmail HTTPS send: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return SendResult{}, fmt.Errorf("gmail read response: %w", err)
	}

	var parsed struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
		Error    struct {
			Code    int                   `json:"code"`
			Message string                `json:"message"`
			Status  string                `json:"status"`
			Errors  []gmailAPIErrorDetail `json:"errors"`
		} `json:"error"`
	}
	decodeErr := json.Unmarshal(respBody, &parsed)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := ""
		if decodeErr == nil {
			message = strings.TrimSpace(parsed.Error.Message)
			if message == "" {
				message = strings.TrimSpace(parsed.Error.Status)
			}
		}
		if message == "" {
			message = resp.Status
		}
		apiErr := fmt.Errorf("gmail API error (%d): %s", resp.StatusCode, redactEmailAddresses(message))
		if resp.StatusCode == http.StatusTooManyRequests ||
			(resp.StatusCode == http.StatusForbidden && gmailRateLimitRejected(parsed.Error.Errors)) {
			return SendResult{}, newRetryableRejectionError(GmailRateLimitRejectedErrorCode, apiErr)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return SendResult{}, fmt.Errorf("%w: %v", ErrAccountUnavailable, apiErr)
		}
		return SendResult{}, apiErr
	}
	if decodeErr != nil {
		return SendResult{}, fmt.Errorf("gmail decode response: %w", decodeErr)
	}

	messageID := strings.TrimSpace(parsed.ID)
	if messageID == "" {
		messageID = "gmail:unavailable"
	}
	result := SendResult{
		ProviderMessageID: messageID,
		ProviderThreadID:  strings.TrimSpace(parsed.ThreadID),
		RFCMessageID:      rfcMessageID,
		FromEmail:         fromEmail,
		ReplyTo:           strings.TrimSpace(req.ReplyTo),
	}
	if !strings.EqualFold(to, originalTo) {
		result.RedirectedTo = to
	}
	return result, nil
}

func gmailRateLimitRejected(details []gmailAPIErrorDetail) bool {
	for _, detail := range details {
		switch strings.ToLower(strings.TrimSpace(detail.Reason)) {
		case "userratelimitexceeded", "ratelimitexceeded", "dailylimitexceeded":
			return true
		}
	}
	return false
}

func resolveGmailFromEmail(requested string, mailboxEmail string, configuredFromEmail string) (string, error) {
	mailboxEmail, err := canonicalMailbox(mailboxEmail)
	if err != nil {
		return "", fmt.Errorf("gmail mailbox address: %w", err)
	}
	configuredFromEmail, err = canonicalMailbox(configuredFromEmail)
	if err != nil {
		return "", fmt.Errorf("gmail configured from address: %w", err)
	}
	if strings.TrimSpace(requested) == "" {
		return configuredFromEmail, nil
	}
	requested, err = canonicalMailbox(requested)
	if err != nil {
		return "", fmt.Errorf("gmail requested from address: %w", err)
	}
	for _, allowed := range []string{mailboxEmail, configuredFromEmail} {
		if requested == allowed {
			return requested, nil
		}
		if isPlusAddressOf(requested, allowed) {
			return allowed, nil
		}
	}
	return "", fmt.Errorf("gmail requested from address is not authorized for this mailbox")
}

func isPlusAddressOf(candidate string, base string) bool {
	candidateLocal, candidateDomain, candidateOK := strings.Cut(candidate, "@")
	baseLocal, baseDomain, baseOK := strings.Cut(base, "@")
	return candidateOK && baseOK &&
		strings.EqualFold(candidateDomain, baseDomain) &&
		strings.HasPrefix(strings.ToLower(candidateLocal), strings.ToLower(baseLocal)+"+")
}

func (provider *gmailProvider) accessToken(ctx context.Context) (string, error) {
	provider.tokenMu.Lock()
	defer provider.tokenMu.Unlock()

	if provider.token != "" && time.Now().UTC().Add(time.Minute).Before(provider.tokenExp) {
		return provider.token, nil
	}

	accessToken, expiresIn, err := provider.refreshAccessToken(ctx)
	if err != nil {
		return "", err
	}
	provider.token = accessToken
	if expiresIn > 0 {
		provider.tokenExp = time.Now().UTC().Add(expiresIn)
	} else {
		provider.tokenExp = time.Time{}
	}
	return accessToken, nil
}

func (provider *gmailProvider) refreshAccessToken(ctx context.Context) (string, time.Duration, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", strings.TrimSpace(provider.cfg.ClientID))
	form.Set("client_secret", strings.TrimSpace(provider.cfg.ClientSecret))
	form.Set("refresh_token", strings.TrimSpace(provider.cfg.RefreshToken))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("gmail token request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := provider.client.Do(httpReq)
	if err != nil {
		return "", 0, fmt.Errorf("gmail token HTTPS request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, fmt.Errorf("gmail token read: %w", err)
	}
	var parsed struct {
		AccessToken      string `json:"access_token"`
		ExpiresIn        int64  `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	decodeErr := json.Unmarshal(respBody, &parsed)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || strings.TrimSpace(parsed.AccessToken) == "" {
		message := ""
		if decodeErr == nil {
			message = strings.TrimSpace(parsed.ErrorDescription)
			if message == "" {
				message = strings.TrimSpace(parsed.Error)
			}
		}
		if message == "" {
			message = resp.Status
		}
		tokenErr := fmt.Errorf("gmail token refresh failed: %s", redactEmailAddresses(message))
		if gmailCredentialRejected(resp.StatusCode, parsed.Error) {
			return "", 0, fmt.Errorf("%w: %v", ErrAccountUnavailable, tokenErr)
		}
		if decodeErr != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return "", 0, fmt.Errorf("gmail token decode: %w", decodeErr)
		}
		return "", 0, tokenErr
	}
	return strings.TrimSpace(parsed.AccessToken), time.Duration(parsed.ExpiresIn) * time.Second, nil
}

func gmailCredentialRejected(statusCode int, oauthError string) bool {
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(oauthError)) {
	case "access_denied", "deleted_client", "invalid_client", "invalid_grant", "invalid_request", "invalid_scope", "unauthorized_client", "unsupported_grant_type":
		return true
	default:
		return false
	}
}

func buildGmailMessage(
	fromEmail string,
	fromName string,
	toEmail string,
	replyTo string,
	subject string,
	textBody string,
	htmlBody string,
	inReplyTo string,
	references string,
) ([]byte, string, error) {
	fromEmail, err := canonicalMailbox(fromEmail)
	if err != nil {
		return nil, "", fmt.Errorf("gmail from address: %w", err)
	}
	toEmail, err = canonicalMailbox(toEmail)
	if err != nil {
		return nil, "", fmt.Errorf("gmail recipient: %w", err)
	}
	fromName, err = cleanHeaderValue(fromName, "from name")
	if err != nil {
		return nil, "", err
	}
	subject, err = cleanHeaderValue(subject, "subject")
	if err != nil {
		return nil, "", err
	}
	inReplyTo, err = cleanHeaderValue(inReplyTo, "in-reply-to")
	if err != nil {
		return nil, "", err
	}
	references, err = cleanHeaderValue(references, "references")
	if err != nil {
		return nil, "", err
	}
	replyTo = strings.TrimSpace(replyTo)
	if replyTo != "" {
		replyTo, err = canonicalMailbox(replyTo)
		if err != nil {
			return nil, "", fmt.Errorf("gmail reply-to: %w", err)
		}
	}
	textBody = strings.TrimSpace(textBody)
	htmlBody = strings.TrimSpace(htmlBody)
	if textBody == "" && htmlBody == "" {
		return nil, "", fmt.Errorf("gmail send: html or text body is required")
	}

	fromDomain := fromEmail
	if at := strings.LastIndex(fromEmail, "@"); at >= 0 {
		fromDomain = fromEmail[at+1:]
	}
	rfcMessageID := fmt.Sprintf("<tuvi.%s@%s>", uuid.NewString(), fromDomain)

	var body bytes.Buffer
	fromHeader := (&mail.Address{Name: fromName, Address: fromEmail}).String()
	toHeader := (&mail.Address{Address: toEmail}).String()
	fmt.Fprintf(&body, "From: %s\r\n", fromHeader)
	fmt.Fprintf(&body, "To: %s\r\n", toHeader)
	fmt.Fprintf(&body, "Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", subject))
	fmt.Fprintf(&body, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&body, "Message-ID: %s\r\n", rfcMessageID)
	if inReplyTo != "" {
		fmt.Fprintf(&body, "In-Reply-To: %s\r\n", inReplyTo)
	}
	if references != "" {
		fmt.Fprintf(&body, "References: %s\r\n", references)
	}
	if replyTo != "" {
		fmt.Fprintf(&body, "Reply-To: %s\r\n", (&mail.Address{Address: replyTo}).String())
	}
	body.WriteString("MIME-Version: 1.0\r\n")

	if textBody != "" && htmlBody != "" {
		writer := multipart.NewWriter(&body)
		contentType := mime.FormatMediaType("multipart/alternative", map[string]string{
			"boundary": writer.Boundary(),
		})
		fmt.Fprintf(&body, "Content-Type: %s\r\n\r\n", contentType)
		if err := writeGmailMIMEPart(writer, "text/plain; charset=UTF-8", textBody); err != nil {
			return nil, "", err
		}
		if err := writeGmailMIMEPart(writer, "text/html; charset=UTF-8", htmlBody); err != nil {
			return nil, "", err
		}
		if err := writer.Close(); err != nil {
			return nil, "", fmt.Errorf("gmail close MIME body: %w", err)
		}
		return body.Bytes(), rfcMessageID, nil
	}

	contentType := "text/plain; charset=UTF-8"
	content := textBody
	if htmlBody != "" {
		contentType = "text/html; charset=UTF-8"
		content = htmlBody
	}
	fmt.Fprintf(&body, "Content-Type: %s\r\n", contentType)
	body.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	encoded := quotedprintable.NewWriter(&body)
	if _, err := encoded.Write([]byte(content)); err != nil {
		return nil, "", fmt.Errorf("gmail encode body: %w", err)
	}
	if err := encoded.Close(); err != nil {
		return nil, "", fmt.Errorf("gmail close encoded body: %w", err)
	}
	return body.Bytes(), rfcMessageID, nil
}

func writeGmailMIMEPart(writer *multipart.Writer, contentType string, content string) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", contentType)
	header.Set("Content-Transfer-Encoding", "quoted-printable")
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("gmail create MIME part: %w", err)
	}
	encoded := quotedprintable.NewWriter(part)
	if _, err := encoded.Write([]byte(content)); err != nil {
		return fmt.Errorf("gmail encode MIME part: %w", err)
	}
	if err := encoded.Close(); err != nil {
		return fmt.Errorf("gmail close MIME part: %w", err)
	}
	return nil
}

func canonicalMailbox(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("email address is required")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || strings.TrimSpace(address.Address) == "" || address.Name != "" {
		return "", fmt.Errorf("email address is invalid")
	}
	return strings.ToLower(strings.TrimSpace(address.Address)), nil
}

func cleanHeaderValue(value string, name string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("gmail %s contains a newline", name)
	}
	return value, nil
}

func validateGmailEndpoint(rawURL string, requireGoogleEndpoint bool, googleHost string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("URL is invalid")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("URL must not contain credentials, query parameters, or fragments")
	}
	if requireGoogleEndpoint {
		if parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), googleHost) {
			return fmt.Errorf("must use the fixed Google HTTPS endpoint")
		}
		return nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("URL must use HTTP or HTTPS")
	}
	return nil
}

var _ Provider = (*gmailProvider)(nil)
