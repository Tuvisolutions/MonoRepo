package outreach

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/auth"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/campaigns"
	emailprovider "github.com/rajchodisetti/restaurant-platform/backend/internal/providers/email"
)

const retryIntegrationSequenceID = "00000000-0000-4000-8000-000000000042"

func openRetryIntegrationPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()

	databaseURL := strings.TrimSpace(os.Getenv("TUVI_OUTREACH_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TUVI_OUTREACH_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect retry integration database: %v", err)
	}
	t.Cleanup(pool.Close)

	var databaseName string
	var sequenceExists bool
	if err := pool.QueryRow(ctx, `
		SELECT current_database(),
		       EXISTS (SELECT 1 FROM outreach_email_sequences WHERE id = $1)`,
		retryIntegrationSequenceID,
	).Scan(&databaseName, &sequenceExists); err != nil {
		t.Fatalf("verify retry integration schema: %v", err)
	}
	if !strings.HasPrefix(databaseName, "tuvi_retry_test") {
		t.Fatalf("refusing retry integration test against database %q", databaseName)
	}
	if !sequenceExists {
		t.Fatalf("retry integration database %q is not migrated through the outreach sequence schema", databaseName)
	}
	return pool, ctx
}

type retryIntegrationFixture struct {
	restaurantID uuid.UUID
	campaignID   uuid.UUID
	accountID    uuid.UUID
	accountKey   string
	jobID        uuid.UUID
	attemptID    uuid.UUID
	workerID     string
}

func seedRetryIntegrationFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	campaignStatus string,
	attemptStatus string,
	withJob bool,
) retryIntegrationFixture {
	t.Helper()

	fixture := retryIntegrationFixture{
		restaurantID: uuid.New(),
		campaignID:   uuid.New(),
		accountID:    uuid.New(),
		accountKey:   "retry-integration-" + uuid.NewString(),
		jobID:        uuid.New(),
		attemptID:    uuid.New(),
		workerID:     "retry-integration-worker-" + uuid.NewString(),
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM restaurants WHERE id = $1`, fixture.restaurantID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM outreach_email_accounts WHERE id = $1`, fixture.accountID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM job_runs WHERE id = $1`, fixture.jobID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO restaurants (id, name, email)
		VALUES ($1, 'Retry integration restaurant', $2)`,
		fixture.restaurantID,
		"retry-"+fixture.restaurantID.String()+"@example.com",
	); err != nil {
		t.Fatalf("insert retry restaurant: %v", err)
	}
	// The enrollment trigger may create a campaign as soon as the
	// policy-complete restaurant is inserted. Replace that fixture-owned row so
	// this test controls the exact campaign state and identifiers below.
	if _, err := pool.Exec(ctx, `DELETE FROM email_campaigns WHERE restaurant_id = $1`, fixture.restaurantID); err != nil {
		t.Fatalf("clear automatic retry campaign enrollment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO outreach_email_accounts (
			id, account_key, provider, provider_identity, from_email,
			position, enabled, send_limit, cycle_number, usage_count,
			cycle_started_at, available_at, send_window_seconds,
			send_jitter_min_seconds, send_jitter_max_seconds, ramp_day
		) VALUES (
			$1, $2, 'gmail', $3, $4,
			0, true, 40, 1, 1,
			now(), now(), 28800,
			120, 300, 1
		)`,
		fixture.accountID,
		fixture.accountKey,
		"gmail|"+fixture.accountKey+"@example.com",
		fixture.accountKey+"@example.com",
	); err != nil {
		t.Fatalf("insert retry account: %v", err)
	}
	if withJob {
		if _, err := pool.Exec(ctx, `
			INSERT INTO job_runs (
				id, job_type, status, payload, attempts, max_attempts,
				available_at, locked_at, locked_by, lease_expires_at
			) VALUES (
				$1, $2, 'running', '{}'::jsonb, 1, 1,
				now(), now(), $3, now() + interval '15 minutes'
			)`,
			fixture.jobID,
			BulkSendJobType,
			fixture.workerID,
		); err != nil {
			t.Fatalf("insert retry bulk job: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO email_campaigns (
			id, restaurant_id, campaign_type, status, current_step,
			subject, body_text, sequence_id, next_step, next_send_at
		) VALUES (
			$1, $2, 'outreach', $3, 0,
			'Retry integration subject', 'Retry integration body', $4, 1, now()
		)`,
		fixture.campaignID,
		fixture.restaurantID,
		campaignStatus,
		retryIntegrationSequenceID,
	); err != nil {
		t.Fatalf("insert retry campaign: %v", err)
	}

	var bulkJobID any
	if withJob {
		bulkJobID = fixture.jobID
	}
	leaseInterval := "5 minutes"
	if attemptStatus != "sending" {
		leaseInterval = ""
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO email_delivery_attempts (
			id, campaign_id, restaurant_id, account_id, bulk_job_id,
			campaign_step, account_cycle, account_sequence, recipient_email,
			status, lease_expires_at
		) VALUES (
			$1, $2, $3, $4, $5,
			1, 1, 1, $6,
			$7, CASE WHEN $8 = '' THEN NULL ELSE now() + ($8::text)::interval END
		)`,
		fixture.attemptID,
		fixture.campaignID,
		fixture.restaurantID,
		fixture.accountID,
		bulkJobID,
		"retry-"+fixture.restaurantID.String()+"@example.com",
		attemptStatus,
		leaseInterval,
	); err != nil {
		t.Fatalf("insert retry delivery attempt: %v", err)
	}
	return fixture
}

func TestRetryableFailureTransactionSchedulesExactStepAndPreservesHistory(t *testing.T) {
	pool, ctx := openRetryIntegrationPool(t)
	fixture := seedRetryIntegrationFixture(t, ctx, pool, campaigns.StatusSending, "sending", true)
	repo := NewPostgres(pool)

	if err := repo.FailEmailDelivery(ctx, emailprovider.DeliveryClaim{
		AttemptID:       fixture.attemptID,
		AccountKey:      fixture.accountKey,
		CampaignStep:    1,
		AccountCycle:    1,
		AccountSequence: 1,
	}, emailprovider.GmailRateLimitRejectedErrorCode); err != nil {
		t.Fatalf("FailEmailDelivery() error = %v", err)
	}

	var attemptStatus string
	var errorCode string
	var attemptLease *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status, error_code, lease_expires_at
		FROM email_delivery_attempts WHERE id = $1`, fixture.attemptID,
	).Scan(&attemptStatus, &errorCode, &attemptLease); err != nil {
		t.Fatalf("load finalized retry attempt: %v", err)
	}
	if attemptStatus != "failed" || errorCode != emailprovider.GmailRateLimitRejectedErrorCode || attemptLease != nil {
		t.Fatalf("attempt = status %q code %q lease %v", attemptStatus, errorCode, attemptLease)
	}

	var campaignStatus string
	var currentStep int
	var nextStep int
	var nextSendAt time.Time
	var stoppedReason string
	if err := pool.QueryRow(ctx, `
		SELECT status, current_step, next_step, next_send_at, stopped_reason
		FROM email_campaigns WHERE id = $1`, fixture.campaignID,
	).Scan(&campaignStatus, &currentStep, &nextStep, &nextSendAt, &stoppedReason); err != nil {
		t.Fatalf("load scheduled retry campaign: %v", err)
	}
	if campaignStatus != campaigns.StatusApproved || currentStep != 0 || nextStep != 1 || stoppedReason != "" {
		t.Fatalf("campaign = status %q current %d next %d reason %q", campaignStatus, currentStep, nextStep, stoppedReason)
	}
	nextLocal := nextSendAt.In(scheduledSendLocation)
	wantDay := time.Now().In(scheduledSendLocation).AddDate(0, 0, 1)
	if nextLocal.Year() != wantDay.Year() || nextLocal.YearDay() != wantDay.YearDay() ||
		nextLocal.Hour() != 7 || nextLocal.Minute() != 0 || nextLocal.Second() != 0 {
		t.Fatalf("retry time = %s, want next Sydney day at 07:00", nextLocal)
	}

	var usageCount int
	var rampDay int
	var cycleNumber int64
	var accountAvailableAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT usage_count, ramp_day, cycle_number, available_at
		FROM outreach_email_accounts WHERE id = $1`, fixture.accountID,
	).Scan(&usageCount, &rampDay, &cycleNumber, &accountAvailableAt); err != nil {
		t.Fatalf("load retry account state: %v", err)
	}
	if usageCount != 1 || rampDay != 1 || cycleNumber != 1 || !accountAvailableAt.Equal(nextSendAt) {
		t.Fatalf("account = usage %d ramp %d cycle %d available %s; retry %s", usageCount, rampDay, cycleNumber, accountAvailableAt, nextSendAt)
	}

	var jobStatus string
	var jobAttempts int
	var lockedBy *string
	var jobLease *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status, attempts, locked_by, lease_expires_at
		FROM job_runs WHERE id = $1`, fixture.jobID,
	).Scan(&jobStatus, &jobAttempts, &lockedBy, &jobLease); err != nil {
		t.Fatalf("load crash-recoverable bulk job: %v", err)
	}
	if jobStatus != "running" || jobAttempts != 0 || lockedBy == nil || *lockedBy != fixture.workerID || jobLease == nil {
		t.Fatalf("job = status %q attempts %d locked_by %v lease %v", jobStatus, jobAttempts, lockedBy, jobLease)
	}

	var failedEvents int
	var retryEvents int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE event_type = 'failed'),
		       count(*) FILTER (WHERE event_type = 'retry_scheduled')
		FROM email_events WHERE delivery_attempt_id = $1`, fixture.attemptID,
	).Scan(&failedEvents, &retryEvents); err != nil {
		t.Fatalf("load retry audit events: %v", err)
	}
	if failedEvents != 1 || retryEvents != 1 {
		t.Fatalf("retry events = failed %d scheduled %d", failedEvents, retryEvents)
	}

	var isContacted bool
	var emailSent bool
	var sendCount int
	if err := pool.QueryRow(ctx, `
		SELECT is_contacted, email_sent, email_send_count
		FROM restaurants WHERE id = $1`, fixture.restaurantID,
	).Scan(&isContacted, &emailSent, &sendCount); err != nil {
		t.Fatalf("load restaurant history fields: %v", err)
	}
	if isContacted || emailSent || sendCount != 0 {
		t.Fatalf("restaurant history was reset/mutated: contacted=%v sent=%v count=%d", isContacted, emailSent, sendCount)
	}
	// Move only the fixture's schedule forward to prove the allowlisted failed
	// attempt itself does not become a permanent list/claim blocker. Production
	// retains the next-window timestamp asserted above.
	if _, err := pool.Exec(ctx, `UPDATE email_campaigns SET next_send_at = now() WHERE id = $1`, fixture.campaignID); err != nil {
		t.Fatalf("make allowlisted retry fixture due: %v", err)
	}
	eligibleCount, err := repo.CountEligibleLeads(ctx)
	if err != nil {
		t.Fatalf("CountEligibleLeads() for allowlisted retry error = %v", err)
	}
	if eligibleCount != 1 {
		t.Fatalf("CountEligibleLeads() for allowlisted retry = %d, want 1", eligibleCount)
	}

	service := &Service{pool: pool}
	if err := service.DeferBulkJob(
		ctx,
		fixture.jobID.String(),
		fixture.workerID,
		uuid.New(),
		BulkSendSummary{Attempted: 1, Failed: 1, NextAvailableAt: &nextSendAt},
	); err != nil {
		t.Fatalf("DeferBulkJob() after retry finalization error = %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT status, attempts, locked_by, lease_expires_at
		FROM job_runs WHERE id = $1`, fixture.jobID,
	).Scan(&jobStatus, &jobAttempts, &lockedBy, &jobLease); err != nil {
		t.Fatalf("load deferred retry bulk job: %v", err)
	}
	if jobStatus != "queued" || jobAttempts != 0 || lockedBy != nil || jobLease != nil {
		t.Fatalf("deferred job = status %q attempts %d locked_by %v lease %v", jobStatus, jobAttempts, lockedBy, jobLease)
	}
}

func TestRetryableFailureTransactionStopsAtInclusiveAttemptCap(t *testing.T) {
	pool, ctx := openRetryIntegrationPool(t)
	fixture := seedRetryIntegrationFixture(t, ctx, pool, campaigns.StatusSending, "sending", true)
	repo := NewPostgres(pool)
	recipient := "retry-" + fixture.restaurantID.String() + "@example.com"

	for _, accountSequence := range []int{2, 3} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO email_delivery_attempts (
				campaign_id, restaurant_id, account_id, campaign_step,
				account_cycle, account_sequence, recipient_email, status, error_code
			) VALUES ($1, $2, $3, 1, 1, $4, $5, 'failed', $6)`,
			fixture.campaignID,
			fixture.restaurantID,
			fixture.accountID,
			accountSequence,
			recipient,
			emailprovider.GmailRateLimitRejectedErrorCode,
		); err != nil {
			t.Fatalf("insert prior failed retry attempt %d: %v", accountSequence, err)
		}
	}

	if err := repo.FailEmailDelivery(ctx, emailprovider.DeliveryClaim{
		AttemptID:       fixture.attemptID,
		AccountKey:      fixture.accountKey,
		CampaignStep:    1,
		AccountCycle:    1,
		AccountSequence: 1,
	}, emailprovider.GmailRateLimitRejectedErrorCode); err != nil {
		t.Fatalf("FailEmailDelivery() at cap error = %v", err)
	}

	var campaignStatus string
	var stoppedReason string
	var nextSendAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status, stopped_reason, next_send_at
		FROM email_campaigns WHERE id = $1`, fixture.campaignID,
	).Scan(&campaignStatus, &stoppedReason, &nextSendAt); err != nil {
		t.Fatalf("load exhausted retry campaign: %v", err)
	}
	if campaignStatus != campaigns.StatusStopped || stoppedReason != emailRetryExhaustedReason || nextSendAt == nil {
		t.Fatalf("exhausted campaign = status %q reason %q next %v", campaignStatus, stoppedReason, nextSendAt)
	}

	var retryExhaustedEvents int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM email_events
		WHERE delivery_attempt_id = $1 AND event_type = 'retry_exhausted'`, fixture.attemptID,
	).Scan(&retryExhaustedEvents); err != nil {
		t.Fatalf("load exhausted retry event: %v", err)
	}
	if retryExhaustedEvents != 1 {
		t.Fatalf("retry_exhausted events = %d, want 1", retryExhaustedEvents)
	}

	var jobStatus string
	var jobAttempts int
	var lockedBy *string
	if err := pool.QueryRow(ctx, `
		SELECT status, attempts, locked_by FROM job_runs WHERE id = $1`, fixture.jobID,
	).Scan(&jobStatus, &jobAttempts, &lockedBy); err != nil {
		t.Fatalf("load exhausted crash-recoverable job: %v", err)
	}
	if jobStatus != "running" || jobAttempts != 0 || lockedBy == nil || *lockedBy != fixture.workerID {
		t.Fatalf("exhausted job = status %q attempts %d locked_by %v", jobStatus, jobAttempts, lockedBy)
	}
}

func TestRetryableFailureTransactionPreservesLaterFollowupHold(t *testing.T) {
	pool, ctx := openRetryIntegrationPool(t)
	fixture := seedRetryIntegrationFixture(t, ctx, pool, campaigns.StatusSending, "sending", true)
	repo := NewPostgres(pool)
	holdUntil := time.Now().UTC().Add(400 * 24 * time.Hour).Truncate(time.Microsecond)

	if _, err := pool.Exec(ctx, `
		UPDATE email_campaigns
		SET current_step = 1, next_step = 2, next_send_at = $2
		WHERE id = $1`, fixture.campaignID, holdUntil); err != nil {
		t.Fatalf("apply later follow-up hold: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE email_delivery_attempts SET campaign_step = 2 WHERE id = $1`, fixture.attemptID); err != nil {
		t.Fatalf("move retry attempt to follow-up step: %v", err)
	}

	if err := repo.FailEmailDelivery(ctx, emailprovider.DeliveryClaim{
		AttemptID:       fixture.attemptID,
		AccountKey:      fixture.accountKey,
		CampaignStep:    2,
		AccountCycle:    1,
		AccountSequence: 1,
	}, emailprovider.GmailPreSendUnavailableErrorCode); err != nil {
		t.Fatalf("FailEmailDelivery() with later hold error = %v", err)
	}

	var campaignStatus string
	var currentStep int
	var nextStep int
	var nextSendAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status, current_step, next_step, next_send_at
		FROM email_campaigns WHERE id = $1`, fixture.campaignID,
	).Scan(&campaignStatus, &currentStep, &nextStep, &nextSendAt); err != nil {
		t.Fatalf("load held follow-up retry: %v", err)
	}
	if campaignStatus != campaigns.StatusApproved || currentStep != 1 || nextStep != 2 || !nextSendAt.Equal(holdUntil) {
		t.Fatalf("held follow-up = status %q current %d next %d at %s, want approved 1 -> 2 at %s", campaignStatus, currentStep, nextStep, nextSendAt, holdUntil)
	}
	var auditedRetryAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT (metadata->>'retry_not_before')::timestamptz
		FROM email_events
		WHERE delivery_attempt_id = $1 AND event_type = 'retry_scheduled'`, fixture.attemptID,
	).Scan(&auditedRetryAt); err != nil {
		t.Fatalf("load held follow-up retry audit: %v", err)
	}
	if !auditedRetryAt.Equal(holdUntil) {
		t.Fatalf("audited retry_not_before = %s, want preserved hold %s", auditedRetryAt, holdUntil)
	}
}

func TestQuotaClaimRejectsUnsafePriorOutcomeBeforeConsumingAnotherSlot(t *testing.T) {
	for _, test := range []struct {
		name         string
		status       string
		errorCode    string
		expectedHold string
	}{
		{name: "unknown provider outcome", status: "unknown", expectedHold: "delivery_unknown"},
		{
			name: "accepted then rate-limit bounced", status: "failed",
			errorCode: "gmail_sender_rate_limit_bounce", expectedHold: "delivery_failure_not_retryable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, ctx := openRetryIntegrationPool(t)
			fixture := seedRetryIntegrationFixture(t, ctx, pool, campaigns.StatusApproved, test.status, false)
			repo := NewPostgres(pool)
			if test.errorCode != "" {
				if _, err := pool.Exec(ctx, `
					UPDATE email_delivery_attempts SET error_code = $2 WHERE id = $1`,
					fixture.attemptID,
					test.errorCode,
				); err != nil {
					t.Fatalf("set unsafe historical failure code: %v", err)
				}
			}

			_, err := repo.ClaimEmailDelivery(ctx, []string{fixture.accountKey}, emailprovider.DeliveryContext{
				CampaignID:   fixture.campaignID,
				RestaurantID: fixture.restaurantID,
				Recipient:    "retry-" + fixture.restaurantID.String() + "@example.com",
				Step:         1,
			}, 24*time.Hour)
			if !errors.Is(err, campaigns.ErrNotEligible) {
				t.Fatalf("ClaimEmailDelivery() error = %v, want campaigns.ErrNotEligible", err)
			}

			var usageCount int
			var attemptCount int
			if err := pool.QueryRow(ctx, `
				SELECT account.usage_count,
				       (SELECT count(*) FROM email_delivery_attempts WHERE campaign_id = $2)
				FROM outreach_email_accounts account WHERE account.id = $1`,
				fixture.accountID,
				fixture.campaignID,
			).Scan(&usageCount, &attemptCount); err != nil {
				t.Fatalf("load rejected claim state: %v", err)
			}
			if usageCount != 1 || attemptCount != 1 {
				t.Fatalf("blocked claim consumed state: usage_count=%d attempt_count=%d", usageCount, attemptCount)
			}
			eligibleCount, err := repo.CountEligibleLeads(ctx)
			if err != nil {
				t.Fatalf("CountEligibleLeads() error = %v", err)
			}
			if eligibleCount != 0 {
				t.Fatalf("CountEligibleLeads() = %d, want unsafe historical outcome excluded", eligibleCount)
			}
			statusCounts, err := repo.CountRecipientStatuses(ctx)
			if err != nil {
				t.Fatalf("CountRecipientStatuses() error = %v", err)
			}
			if statusCounts.DueFollowups != 0 || statusCounts.NewRecipients != 0 || statusCounts.Paused < 1 {
				t.Fatalf("recipient status counts = %#v, want unsafe outcome paused", statusCounts)
			}

			service := &Service{pool: pool}
			progress, err := service.ListRecipientProgress(ctx, auth.Principal{Role: auth.RoleInternalAdmin}, 500, 0)
			if err != nil {
				t.Fatalf("ListRecipientProgress() error = %v", err)
			}
			found := false
			for _, recipient := range progress.Recipients {
				if recipient.RestaurantID != fixture.restaurantID {
					continue
				}
				found = true
				if recipient.Eligible || recipient.HoldReason != test.expectedHold {
					t.Fatalf("recipient progress = eligible %v hold %q, want paused %q", recipient.Eligible, recipient.HoldReason, test.expectedHold)
				}
			}
			if !found {
				t.Fatal("retry fixture was missing from recipient progress")
			}
		})
	}
}
