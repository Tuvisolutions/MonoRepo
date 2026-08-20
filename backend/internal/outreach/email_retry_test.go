package outreach

import (
	"strings"
	"testing"
	"time"

	emailprovider "github.com/rajchodisetti/restaurant-platform/backend/internal/providers/email"
)

func TestDecideEmailFailureRetryUsesExplicitDefinitiveAllowlist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    string
		errorCode string
		retryable bool
		schedule  bool
		exhausted bool
	}{
		{
			name:      "gmail request rate limit rejection",
			status:    "failed",
			errorCode: emailprovider.GmailRateLimitRejectedErrorCode,
			retryable: true,
			schedule:  true,
		},
		{
			name:      "credential rejection with normalized input",
			status:    "failed",
			errorCode: "  CREDENTIAL_OR_AUTHORIZATION_REJECTED  ",
			retryable: true,
			schedule:  true,
		},
		{
			name:      "transient Gmail access failure before send",
			status:    "failed",
			errorCode: emailprovider.GmailPreSendUnavailableErrorCode,
			retryable: true,
			schedule:  true,
		},
		{
			name:      "generic rejection is not allowlisted",
			status:    "failed",
			errorCode: "provider_rejected_before_acceptance",
		},
		{
			name:      "accepted then bounced is not safe to retry",
			status:    "failed",
			errorCode: "gmail_sender_rate_limit_bounce",
		},
		{
			name:      "unknown outcome never retries",
			status:    "unknown",
			errorCode: emailprovider.GmailRateLimitRejectedErrorCode,
		},
		{
			name:      "accepted send never retries",
			status:    "sent",
			errorCode: emailprovider.GmailRateLimitRejectedErrorCode,
		},
		{
			name:      "skip never enters failure retry policy",
			status:    "skipped",
			errorCode: emailprovider.GmailRateLimitRejectedErrorCode,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := decideEmailFailureRetry(test.status, test.errorCode, 1)
			if got.Retryable != test.retryable || got.Schedule != test.schedule || got.Exhausted != test.exhausted {
				t.Fatalf("decision = %#v, want retryable=%v schedule=%v exhausted=%v", got, test.retryable, test.schedule, test.exhausted)
			}
		})
	}
}

func TestDecideEmailFailureRetryStopsAtThreeTotalStepAttempts(t *testing.T) {
	t.Parallel()

	if maxEmailDeliveryAttemptsPerStep != 3 {
		t.Fatalf("maximum attempts = %d, want 3", maxEmailDeliveryAttemptsPerStep)
	}
	for _, test := range []struct {
		attempts  int
		schedule  bool
		exhausted bool
	}{
		{attempts: 1, schedule: true},
		{attempts: 2, schedule: true},
		{attempts: 3, exhausted: true},
		{attempts: 4, exhausted: true},
	} {
		got := decideEmailFailureRetry("failed", emailprovider.GmailRateLimitRejectedErrorCode, test.attempts)
		if !got.Retryable || got.Schedule != test.schedule || got.Exhausted != test.exhausted {
			t.Fatalf("attempts %d decision = %#v, want schedule=%v exhausted=%v", test.attempts, got, test.schedule, test.exhausted)
		}
	}
	if got := decideEmailFailureRetry("failed", emailprovider.GmailRateLimitRejectedErrorCode, 0); got != (emailFailureRetryDecision{}) {
		t.Fatalf("invalid zero-attempt decision = %#v, want no retry", got)
	}
}

func TestNextEmailDeliveryRetryAtUsesNextSydneyDayAcrossDST(t *testing.T) {
	t.Parallel()

	schedule := newStoredEmailSendSchedule(8*60+15, 13*60+45, nil, time.Time{})
	tests := []struct {
		name      string
		failedAt  time.Time
		wantLocal string
	}{
		{
			name:      "daylight saving begins",
			failedAt:  time.Date(2026, time.October, 3, 10, 0, 0, 0, scheduledSendLocation),
			wantLocal: "2026-10-04 08:15 AEDT",
		},
		{
			name:      "daylight saving ends",
			failedAt:  time.Date(2026, time.April, 4, 10, 0, 0, 0, scheduledSendLocation),
			wantLocal: "2026-04-05 08:15 AEST",
		},
		{
			name:      "failure before today's window still waits one day",
			failedAt:  time.Date(2026, time.August, 15, 6, 0, 0, 0, scheduledSendLocation),
			wantLocal: "2026-08-16 08:15 AEST",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := nextEmailDeliveryRetryAt(test.failedAt.UTC(), schedule)
			if gotLocal := got.In(scheduledSendLocation).Format("2006-01-02 15:04 MST"); gotLocal != test.wantLocal {
				t.Fatalf("retry time = %s, want %s", gotLocal, test.wantLocal)
			}
		})
	}
}

func TestRetryPersistenceKeepsExactStepAndImmutableQuotaHistory(t *testing.T) {
	t.Parallel()

	for name, query := range map[string]string{
		"scheduled retry":       scheduleRetryableEmailCampaignQuery,
		"exhausted retry":       stopExhaustedEmailCampaignQuery,
		"non-retryable failure": stopNonRetryableEmailCampaignQuery,
	} {
		setClause := strings.Split(query, "WHERE")[0]
		for _, forbidden := range []string{"current_step", "next_step"} {
			if strings.Contains(setClause, forbidden) {
				t.Fatalf("%s mutates %s: %q", name, forbidden, setClause)
			}
		}
		for _, required := range []string{"current_step < $2", "next_step = $2"} {
			if !strings.Contains(query, required) {
				t.Fatalf("%s does not retain the claimed step through %q", name, required)
			}
		}
	}
	for _, required := range []string{
		"next_send_at = GREATEST(next_send_at, $5)",
		"stopped_reason = ''",
	} {
		if !strings.Contains(scheduleRetryableEmailCampaignQuery, required) {
			t.Fatalf("retry schedule does not preserve the claimed step through %q", required)
		}
	}
	for _, required := range []string{"campaign_id = $1", "campaign_step = $2"} {
		if !strings.Contains(countEmailDeliveryAttemptsForStepQuery, required) {
			t.Fatalf("attempt cap query missing %q", required)
		}
	}
	for _, required := range []string{
		"status <> 'skipped'",
		"status IN ('sent', 'sending', 'unknown')",
		"status = 'failed'",
		"lower(trim(error_code)) NOT IN",
		emailprovider.GmailRateLimitRejectedErrorCode,
		emailprovider.GmailPreSendUnavailableErrorCode,
		emailprovider.AccountUnavailableErrorCode,
	} {
		if !strings.Contains(countEmailDeliveryAttemptsForStepQuery, required) {
			t.Fatalf("attempt cap query missing outcome guard %q", required)
		}
	}

	for _, forbidden := range []string{"usage_count", "ramp_day", "cycle_number", "DELETE"} {
		if strings.Contains(coolRetryableEmailAccountQuery, forbidden) {
			t.Fatalf("mailbox cooldown mutates quota/history through %q", forbidden)
		}
	}
	if !strings.Contains(coolRetryableEmailAccountQuery, "GREATEST(available_at, $2)") {
		t.Fatal("mailbox cooldown can move availability backwards")
	}
	if emailRetryExhaustedReason != "delivery_retry_exhausted" {
		t.Fatalf("exhausted reason = %q", emailRetryExhaustedReason)
	}
}

func TestQuotaClaimBlocksAmbiguousAcceptedAndExhaustedStepsBeforeProviderBoundary(t *testing.T) {
	t.Parallel()

	for _, required := range []string{
		"prior_attempt.status IN ('sent', 'sending', 'unknown')",
		"prior_attempt.status = 'failed'",
		"lower(trim(prior_attempt.error_code)) NOT IN",
		"prior_attempt.status <> 'skipped'",
	} {
		if !strings.Contains(emailDeliveryClaimCampaignQuery, required) {
			t.Fatalf("quota claim missing fail-closed guard %q", required)
		}
	}
}
