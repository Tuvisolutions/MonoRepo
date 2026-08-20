package outreach

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/campaigns"
	platformdb "github.com/rajchodisetti/restaurant-platform/backend/internal/platform/db"
	emailprovider "github.com/rajchodisetti/restaurant-platform/backend/internal/providers/email"
)

const (
	emailDeliveryLease              = 5 * time.Minute
	emailRampStep                   = 5
	maxEmailDeliveryAttemptsPerStep = 3
	emailRetryExhaustedReason       = "delivery_retry_exhausted"
	emailFailureNotRetryableReason  = "delivery_failure_not_retryable"
	emailFailureOutcomeConflict     = "delivery_outcome_conflict"
	emailRetryScheduledEvent        = "retry_scheduled"
	emailRetryExhaustedEvent        = "retry_exhausted"
	retryableEmailFailureCodesSQL   = "'" + emailprovider.GmailRateLimitRejectedErrorCode + "', '" +
		emailprovider.GmailPreSendUnavailableErrorCode + "', '" + emailprovider.AccountUnavailableErrorCode + "'"
)

var errDeliveryAttemptNotSending = errors.New("delivery attempt is not in sending state")

var scheduledSendLocation = mustLoadScheduledSendLocation()

type scheduledEmailWindow struct {
	Start time.Time
	End   time.Time
	Open  bool
}

func mustLoadScheduledSendLocation() *time.Location {
	location, err := time.LoadLocation(scheduledSendTimezone)
	if err != nil {
		return time.FixedZone("AEST", 10*60*60)
	}
	return location
}

func emailWindowAt(now time.Time, schedule storedEmailSendSchedule) scheduledEmailWindow {
	localNow := now.In(scheduledSendLocation)
	startHour := schedule.startMinute / 60
	startMinute := schedule.startMinute % 60
	endHour := schedule.endMinute / 60
	endMinute := schedule.endMinute % 60
	start := time.Date(
		localNow.Year(), localNow.Month(), localNow.Day(),
		startHour, startMinute, 0, 0,
		scheduledSendLocation,
	)
	end := time.Date(
		localNow.Year(), localNow.Month(), localNow.Day(),
		endHour, endMinute, 0, 0,
		scheduledSendLocation,
	)
	if !localNow.Before(start) && localNow.Before(end) {
		return scheduledEmailWindow{Start: start.UTC(), End: end.UTC(), Open: true}
	}
	if !localNow.Before(end) {
		start = start.AddDate(0, 0, 1)
		end = end.AddDate(0, 0, 1)
	}
	return scheduledEmailWindow{Start: start.UTC(), End: end.UTC()}
}

func nextScheduledSendAt(now time.Time, candidate time.Time, schedule storedEmailSendSchedule) time.Time {
	if candidate.Before(now) {
		candidate = now
	}
	window := emailWindowAt(candidate, schedule)
	if window.Open {
		return candidate.UTC()
	}
	return window.Start
}

// nextEmailDeliveryRetryAt returns the beginning of the next Sydney calendar
// day's saved send window. A definitive failure never re-enters eligibility on
// the same day, even when the current window is still open. Constructing the
// next local date before converting to UTC preserves the saved wall-clock time
// across daylight-saving transitions.
func nextEmailDeliveryRetryAt(now time.Time, schedule storedEmailSendSchedule) time.Time {
	localTomorrow := now.In(scheduledSendLocation).AddDate(0, 0, 1)
	return time.Date(
		localTomorrow.Year(),
		localTomorrow.Month(),
		localTomorrow.Day(),
		schedule.startMinute/60,
		schedule.startMinute%60,
		0,
		0,
		scheduledSendLocation,
	).UTC()
}

type emailFailureRetryDecision struct {
	Retryable bool
	Schedule  bool
	Exhausted bool
}

// decideEmailFailureRetry deliberately admits only provider responses that
// prove rejection before acceptance. Unknown outcomes, accepted sends, skips,
// generic failures, and later bounces must never enter this automatic path.
func decideEmailFailureRetry(status, errorCode string, totalAttempts int) emailFailureRetryDecision {
	if status != "failed" || totalAttempts < 1 {
		return emailFailureRetryDecision{}
	}
	switch strings.ToLower(strings.TrimSpace(errorCode)) {
	case emailprovider.GmailRateLimitRejectedErrorCode,
		emailprovider.GmailPreSendUnavailableErrorCode,
		emailprovider.AccountUnavailableErrorCode:
		if totalAttempts >= maxEmailDeliveryAttemptsPerStep {
			return emailFailureRetryDecision{Retryable: true, Exhausted: true}
		}
		return emailFailureRetryDecision{Retryable: true, Schedule: true}
	default:
		return emailFailureRetryDecision{}
	}
}

func rampedSendLimit(maxLimit, rampDay int) int {
	if maxLimit < 1 {
		return 0
	}
	if rampDay < 1 {
		rampDay = 1
	}
	daysToLimit := (maxLimit + emailRampStep - 1) / emailRampStep
	if rampDay >= daysToLimit {
		return maxLimit
	}
	return rampDay * emailRampStep
}

func nextEmailRampDay(maxLimit, rampDay int) int {
	if rampDay < 1 {
		rampDay = 1
	}
	if rampedSendLimit(maxLimit, rampDay) >= maxLimit {
		return rampDay
	}
	return rampDay + 1
}

type SequenceDeliveryFinalization struct {
	CampaignID        uuid.UUID
	RestaurantID      uuid.UUID
	Step              int
	RecipientEmail    string
	Outcome           string
	ProviderMessageID string
	Skipped           bool
	Redirected        bool
	ErrorCode         string
}

const (
	sequenceOutcomeSent    = "sent"
	sequenceOutcomeSkipped = "skipped"
	sequenceOutcomeUnknown = "unknown"
)

// FinalizeSequenceDelivery applies the same progression rules for local/fake
// account pools that durable quota-managed delivery applies after a provider
// outcome. Only a confirmed acceptance advances the sequence. A skip retains
// the due step; an ambiguous provider failure retains it and fails closed as
// send_unknown for operator reconciliation.
func (repo *Postgres) FinalizeSequenceDelivery(
	ctx context.Context,
	finalization SequenceDeliveryFinalization,
) error {
	if repo.pool == nil {
		return fmt.Errorf("database pool is not configured")
	}
	if finalization.CampaignID == uuid.Nil || finalization.RestaurantID == uuid.Nil || finalization.Step < 1 {
		return fmt.Errorf("campaign, restaurant, and a 1-based step are required")
	}
	if finalization.Outcome != sequenceOutcomeSent &&
		finalization.Outcome != sequenceOutcomeSkipped &&
		finalization.Outcome != sequenceOutcomeUnknown {
		return fmt.Errorf("unsupported sequence delivery outcome %q", finalization.Outcome)
	}

	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin sequence delivery finalization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := platformdb.LockRestaurantWorkflow(ctx, tx, finalization.RestaurantID); err != nil {
		return err
	}

	var currentRestaurantID uuid.UUID
	var currentStep int
	var nextStep *int
	var campaignStatus string
	if err := tx.QueryRow(ctx, `
		SELECT restaurant_id, current_step, next_step, status
		FROM email_campaigns
		WHERE id = $1 AND sequence_id IS NOT NULL
		FOR UPDATE`, finalization.CampaignID).Scan(
		&currentRestaurantID, &currentStep, &nextStep, &campaignStatus,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: sequence campaign is unavailable", campaigns.ErrNotEligible)
		}
		return fmt.Errorf("lock sequence campaign: %w", err)
	}
	if currentRestaurantID != finalization.RestaurantID || nextStep == nil || *nextStep != finalization.Step || currentStep >= finalization.Step {
		return fmt.Errorf("%w: sequence step changed before finalization", campaigns.ErrNotEligible)
	}
	if campaignStatus != campaigns.StatusApproved && campaignStatus != campaigns.StatusStopped {
		return fmt.Errorf("%w: sequence campaign is not awaiting delivery", campaigns.ErrNotEligible)
	}

	eventType := campaigns.EventFailed
	if finalization.Outcome == sequenceOutcomeSent {
		eventType = campaigns.EventSent
	} else if finalization.Outcome == sequenceOutcomeSkipped {
		eventType = campaigns.EventSkipped
	}
	metadata, err := json.Marshal(map[string]any{
		"step":             finalization.Step,
		"provider_message": strings.TrimSpace(finalization.ProviderMessageID),
		"bulk_outreach":    true,
		"quota_managed":    false,
		"skipped":          finalization.Skipped,
		"redirected":       finalization.Redirected,
		"error_code":       strings.TrimSpace(finalization.ErrorCode),
	})
	if err != nil {
		return fmt.Errorf("encode sequence delivery event: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO email_events (campaign_id, restaurant_id, event_type, metadata)
		VALUES ($1, $2, $3, $4)`,
		finalization.CampaignID, finalization.RestaurantID, eventType, metadata,
	); err != nil {
		return fmt.Errorf("insert sequence delivery event: %w", err)
	}

	if finalization.Outcome == sequenceOutcomeSent {
		result, err := tx.Exec(ctx, `
			WITH next_enabled AS (
			  SELECT step.position, step.delay_hours
			  FROM email_campaigns current_campaign
			  JOIN outreach_email_sequence_steps step
			    ON step.sequence_id = current_campaign.sequence_id
			  WHERE current_campaign.id = $1
			    AND step.enabled = true
			    AND step.position > $3
			  ORDER BY step.position
			  LIMIT 1
			)
			UPDATE email_campaigns campaign
			SET current_step = $3,
			    next_step = CASE
			      WHEN campaign.status = $4 THEN NULL
			      ELSE (SELECT position FROM next_enabled)
			    END,
			    next_send_at = CASE
			      WHEN campaign.status = $4 OR NOT EXISTS (SELECT 1 FROM next_enabled) THEN NULL
			      ELSE now() + ((SELECT delay_hours FROM next_enabled) * interval '1 hour')
			    END,
			    completed_at = CASE
			      WHEN campaign.status = $4 OR NOT EXISTS (SELECT 1 FROM next_enabled) THEN now()
			      ELSE NULL
			    END,
			    status = CASE
			      WHEN campaign.status = $4 THEN $4
			      WHEN EXISTS (SELECT 1 FROM next_enabled) THEN $5
			      ELSE $6
			    END,
			    last_sent_at = now(),
			    updated_at = now()
			WHERE campaign.id = $1
			  AND campaign.restaurant_id = $2
			  AND campaign.next_step = $3
			  AND campaign.status IN ($4, $5)`,
			finalization.CampaignID,
			finalization.RestaurantID,
			finalization.Step,
			campaigns.StatusStopped,
			campaigns.StatusApproved,
			campaigns.StatusSent,
		)
		if err != nil {
			return fmt.Errorf("advance sequence campaign: %w", err)
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("%w: sequence campaign changed before advancement", campaigns.ErrNotEligible)
		}
		result, err = tx.Exec(ctx, `
			UPDATE restaurants
			SET is_contacted = true,
			    email_sent = true,
			    email_send_count = email_send_count + 1,
			    last_email_sent_at = now(),
			    last_email_send_sequence = nextval('email_send_sequence'),
			    last_email_recipient = lower(trim($2)),
			    status = CASE WHEN status = 'lead' THEN 'emailed' ELSE status END,
			    updated_at = now()
			WHERE id = $1`, finalization.RestaurantID, finalization.RecipientEmail)
		if err != nil {
			return fmt.Errorf("record confirmed restaurant email: %w", err)
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("restaurant was not found while recording confirmed email")
		}
	} else if finalization.Outcome == sequenceOutcomeUnknown && campaignStatus != campaigns.StatusStopped {
		if _, err := tx.Exec(ctx, `
			UPDATE email_campaigns
			SET status = $2, updated_at = now()
			WHERE id = $1 AND status = $3`,
			finalization.CampaignID, campaigns.StatusSendUnknown, campaigns.StatusApproved,
		); err != nil {
			return fmt.Errorf("mark sequence delivery unknown: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit sequence delivery finalization: %w", err)
	}
	return nil
}

func (repo *Postgres) SyncEmailAccounts(
	ctx context.Context,
	accounts []emailprovider.QuotaAccountConfig,
	cooldown time.Duration,
) error {
	if repo.pool == nil {
		return fmt.Errorf("database pool is not configured")
	}
	if len(accounts) == 0 {
		return fmt.Errorf("at least one outreach email account is required")
	}
	if cooldown < 24*time.Hour {
		return fmt.Errorf("outreach email cooldown must be at least 24h")
	}

	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin email account sync: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, account := range accounts {
		key := strings.TrimSpace(account.Key)
		if key == "" {
			return fmt.Errorf("outreach email account key is required")
		}
		if account.SendLimit < 1 || account.SendLimit > 40 {
			return fmt.Errorf("outreach email account %q send limit must be between 1 and 40", key)
		}
		if account.SendWindow < 8*time.Hour {
			return fmt.Errorf("outreach email account %q send window must be at least 8h", key)
		}
		if account.SendJitterMin < 2*time.Minute {
			return fmt.Errorf("outreach email account %q minimum send jitter must be at least 2m", key)
		}
		if account.SendJitterMax < account.SendJitterMin {
			return fmt.Errorf("outreach email account %q maximum send jitter must not be less than its minimum", key)
		}
		if account.SendWindow/time.Duration(account.SendLimit) <= account.SendJitterMax {
			return fmt.Errorf("outreach email account %q slot width must be greater than its maximum send jitter", key)
		}
		if account.SendWindow%time.Second != 0 || account.SendJitterMin%time.Second != 0 || account.SendJitterMax%time.Second != 0 {
			return fmt.Errorf("outreach email account %q pacing durations must use whole seconds", key)
		}
		provider := strings.TrimSpace(account.Provider)
		providerIdentity := strings.ToLower(strings.TrimSpace(account.ProviderIdentity))
		fromEmail := strings.TrimSpace(account.FromEmail)
		if provider == "" || providerIdentity == "" || fromEmail == "" || account.Position < 0 {
			return fmt.Errorf("outreach email account %q has invalid provider metadata", key)
		}
		const upsert = `
			INSERT INTO outreach_email_accounts (
				account_key, provider, provider_identity, from_email, position, enabled, send_limit,
				send_window_seconds, send_jitter_min_seconds, send_jitter_max_seconds
			) VALUES ($1, $2, $3, $4, $5, true, $6, $8, $9, $10)
			ON CONFLICT (provider_identity) DO UPDATE
			SET account_key = EXCLUDED.account_key,
			    provider = EXCLUDED.provider,
			    from_email = EXCLUDED.from_email,
			    position = EXCLUDED.position,
			    enabled = true,
			    send_limit = EXCLUDED.send_limit,
			    send_window_seconds = EXCLUDED.send_window_seconds,
			    send_jitter_min_seconds = EXCLUDED.send_jitter_min_seconds,
			    send_jitter_max_seconds = EXCLUDED.send_jitter_max_seconds,
			    usage_count = LEAST(outreach_email_accounts.usage_count, EXCLUDED.send_limit),
			    available_at = CASE
			      WHEN outreach_email_accounts.send_limit > EXCLUDED.send_limit
			       AND outreach_email_accounts.usage_count >= EXCLUDED.send_limit
			       AND outreach_email_accounts.available_at <= now()
			      THEN now() + ($7 * interval '1 second')
			      ELSE outreach_email_accounts.available_at
			    END,
			    updated_at = now()`
		if _, err := tx.Exec(
			ctx,
			upsert,
			key,
			provider,
			providerIdentity,
			fromEmail,
			account.Position,
			account.SendLimit,
			int64(cooldown/time.Second),
			int64(account.SendWindow/time.Second),
			int64(account.SendJitterMin/time.Second),
			int64(account.SendJitterMax/time.Second),
		); err != nil {
			return fmt.Errorf("sync outreach email account %q: %w", key, err)
		}
	}

	schedule, err := loadEmailSendSchedule(ctx, tx, false)
	if err != nil {
		return err
	}
	totalDailyLimit := 0
	largestMailboxPacingRequirement := time.Duration(0)
	for _, account := range accounts {
		totalDailyLimit += account.SendLimit
		mailboxPacingRequirement := time.Duration(account.SendLimit) * account.SendJitterMin
		if mailboxPacingRequirement > largestMailboxPacingRequirement {
			largestMailboxPacingRequirement = mailboxPacingRequirement
		}
	}
	if err := validateEmailScheduleCapacity(
		time.Duration(schedule.endMinute-schedule.startMinute)*time.Minute,
		totalDailyLimit,
		largestMailboxPacingRequirement,
	); err != nil {
		return err
	}
	// This table is the pacing mirror, not the credential source of truth. Mark
	// entries absent from the effective provider set inactive so UI schedule
	// capacity validation does not count removed or disabled mailboxes.
	accountKeys := make([]string, 0, len(accounts))
	for _, account := range accounts {
		accountKeys = append(accountKeys, strings.TrimSpace(account.Key))
	}
	if _, err := tx.Exec(ctx, `
		UPDATE outreach_email_accounts
		SET enabled = false, updated_at = now()
		WHERE enabled = true AND NOT (account_key = ANY($1::text[]))`, accountKeys); err != nil {
		return fmt.Errorf("disable removed outreach email account pacing rows: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit email account sync: %w", err)
	}
	return nil
}

const emailDeliveryClaimCampaignQuery = `
	SELECT campaign.restaurant_id,
	       campaign.status,
	       campaign.campaign_type,
	       campaign.subject,
	       campaign.body_html,
	       campaign.body_text,
	       campaign.demo_token,
	       restaurant.name,
	       restaurant.email,
	       restaurant.status,
	       restaurant.shown_interest,
	       restaurant.outreach_consent_basis,
	       restaurant.outreach_consent_source,
	       restaurant.outreach_consent_recorded_at,
	       restaurant.outreach_consent_evidence,
	       campaign.next_step,
	       campaign.next_send_at,
	       sequence.status,
	       sequence.approved_at IS NOT NULL,
	       step.enabled,
	       EXISTS (
	         SELECT 1
	         FROM email_delivery_attempts prior_attempt
	         WHERE prior_attempt.campaign_id = campaign.id
	           AND prior_attempt.campaign_step = campaign.next_step
	           AND (
	             prior_attempt.status IN ('sent', 'sending', 'unknown')
	             OR (
	               prior_attempt.status = 'failed'
	               AND lower(trim(prior_attempt.error_code)) NOT IN (` + retryableEmailFailureCodesSQL + `)
	             )
	           )
	       ),
	       (
	         SELECT count(*)
	         FROM email_delivery_attempts prior_attempt
	         WHERE prior_attempt.campaign_id = campaign.id
	           AND prior_attempt.campaign_step = campaign.next_step
	           AND prior_attempt.status <> 'skipped'
	       )
	FROM email_campaigns AS campaign
	JOIN restaurants AS restaurant ON restaurant.id = campaign.restaurant_id
	JOIN outreach_email_sequences AS sequence ON sequence.id = campaign.sequence_id
	JOIN outreach_email_sequence_steps AS step
	  ON step.sequence_id = campaign.sequence_id
	 AND step.position = campaign.next_step
	WHERE campaign.id = $1
	FOR UPDATE OF campaign, restaurant`

func (repo *Postgres) ClaimEmailDelivery(
	ctx context.Context,
	accountKeys []string,
	delivery emailprovider.DeliveryContext,
	cooldown time.Duration,
) (emailprovider.DeliveryClaim, error) {
	if repo.pool == nil {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("database pool is not configured")
	}
	if len(accountKeys) == 0 {
		return emailprovider.DeliveryClaim{}, emailprovider.ErrAccountsExhausted
	}
	if delivery.CampaignID == uuid.Nil || delivery.RestaurantID == uuid.Nil {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("campaign and restaurant are required for quota-managed delivery")
	}
	if delivery.Step < 0 {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("campaign step cannot be negative")
	}
	if cooldown < 24*time.Hour {
		cooldown = 24 * time.Hour
	}
	if _, err := repo.ReconcileStaleEmailDeliveries(ctx); err != nil {
		return emailprovider.DeliveryClaim{}, err
	}

	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("begin email quota claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := platformdb.LockRestaurantWorkflow(ctx, tx, delivery.RestaurantID); err != nil {
		return emailprovider.DeliveryClaim{}, err
	}

	var restaurantID uuid.UUID
	var campaignStatus string
	var campaignType string
	var campaignSubject string
	var campaignBodyHTML string
	var campaignBodyText string
	var campaignDemoToken string
	var restaurantName string
	var restaurantEmail string
	var lifecycleStatus string
	var shownInterest bool
	var consentBasis string
	var consentSource string
	var consentRecordedAt *time.Time
	var consentEvidence json.RawMessage
	var nextStep *int
	var nextSendAt *time.Time
	var sequenceStatus string
	var sequenceApprovalAudited bool
	var stepEnabled bool
	var blockingStepOutcome bool
	var priorStepAttempts int
	if err := tx.QueryRow(ctx, emailDeliveryClaimCampaignQuery, delivery.CampaignID).Scan(
		&restaurantID,
		&campaignStatus,
		&campaignType,
		&campaignSubject,
		&campaignBodyHTML,
		&campaignBodyText,
		&campaignDemoToken,
		&restaurantName,
		&restaurantEmail,
		&lifecycleStatus,
		&shownInterest,
		&consentBasis,
		&consentSource,
		&consentRecordedAt,
		&consentEvidence,
		&nextStep,
		&nextSendAt,
		&sequenceStatus,
		&sequenceApprovalAudited,
		&stepEnabled,
		&blockingStepOutcome,
		&priorStepAttempts,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return emailprovider.DeliveryClaim{}, fmt.Errorf("%w: campaign review context is unavailable", campaigns.ErrNotEligible)
		}
		return emailprovider.DeliveryClaim{}, fmt.Errorf("lock outreach campaign: %w", err)
	}
	if restaurantID != delivery.RestaurantID {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("campaign does not belong to restaurant")
	}
	if campaignType != campaigns.TypeOutreach {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("quota-managed delivery requires an outreach campaign")
	}
	if campaignStatus != campaigns.StatusApproved {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("%w: campaign must remain approved while claiming email quota", campaigns.ErrNotEligible)
	}
	if nextStep == nil || *nextStep != delivery.Step || nextSendAt == nil || nextSendAt.After(time.Now().UTC()) {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("%w: sequence step is not due", campaigns.ErrNotEligible)
	}
	if !sequenceApprovalAudited || (sequenceStatus != SequenceStatusApproved && sequenceStatus != SequenceStatusArchived) || !stepEnabled {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("%w: sequence version or step is not approved", campaigns.ErrNotEligible)
	}
	if blockingStepOutcome {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("%w: sequence step has an unsafe prior delivery outcome", campaigns.ErrNotEligible)
	}
	if priorStepAttempts >= maxEmailDeliveryAttemptsPerStep {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("%w: sequence step reached the delivery attempt cap", campaigns.ErrNotEligible)
	}
	actualRecipient := strings.ToLower(strings.TrimSpace(restaurantEmail))
	expectedRecipient := strings.ToLower(strings.TrimSpace(delivery.Recipient))
	if expectedRecipient == "" || expectedRecipient != actualRecipient {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("%w: outreach recipient changed before quota claim", campaigns.ErrNotEligible)
	}
	expectedArtifact := strings.TrimSpace(delivery.CampaignArtifactFingerprint)
	currentArtifact := emailprovider.CampaignArtifactFingerprint(
		campaignSubject,
		campaignBodyHTML,
		campaignBodyText,
		campaignDemoToken,
	)
	if expectedArtifact == "" || expectedArtifact != currentArtifact {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("%w: campaign content changed before quota claim", campaigns.ErrNotEligible)
	}

	if err := checkSequenceDeliveryEligibility(SequenceDelivery{
		RestaurantName:    restaurantName,
		RecipientEmail:    restaurantEmail,
		LifecycleStatus:   lifecycleStatus,
		ShownInterest:     shownInterest,
		ConsentBasis:      consentBasis,
		ConsentSource:     consentSource,
		ConsentRecordedAt: consentRecordedAt,
		ConsentEvidence:   consentEvidence,
		SequenceStatus:    sequenceStatus,
		Step:              SequenceStep{Position: delivery.Step, Enabled: stepEnabled},
	}); err != nil {
		return emailprovider.DeliveryClaim{}, err
	}

	// The singleton gate serializes every provider-boundary reservation across
	// all configured mailboxes. It prevents an account transition or a second
	// worker from bypassing the random velocity guard.
	var globalNextSendAt time.Time
	var now time.Time
	var scheduleStartMinute int
	var scheduleEndMinute int
	if err := tx.QueryRow(
		ctx,
		`SELECT pacing.next_send_at, clock_timestamp(), schedule.start_minute, schedule.end_minute
		 FROM outreach_email_pacing AS pacing
		 CROSS JOIN outreach_send_schedule AS schedule
		 WHERE pacing.singleton = 1 AND schedule.singleton = 1
		 FOR UPDATE OF pacing, schedule`,
	).Scan(&globalNextSendAt, &now, &scheduleStartMinute, &scheduleEndMinute); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return emailprovider.DeliveryClaim{}, fmt.Errorf("outreach email pacing state is not initialized")
		}
		return emailprovider.DeliveryClaim{}, fmt.Errorf("lock global outreach email pacing: %w", err)
	}
	now = now.UTC()
	schedule := newStoredEmailSendSchedule(scheduleStartMinute, scheduleEndMinute, nil, time.Time{})
	window := emailWindowAt(now, schedule)
	if !window.Open {
		return emailprovider.DeliveryClaim{}, emailprovider.ErrAccountsExhausted
	}
	if globalNextSendAt.After(now) {
		return emailprovider.DeliveryClaim{}, emailprovider.ErrAccountsExhausted
	}

	// Select the mailbox whose next daily slot is due first. Mailboxes advance
	// together through the saved Sydney window instead of serializing complete
	// per-account windows, which lets the aggregate daily allowance finish by
	// the configured end time while preserving each mailbox's own cadence.
	const selectAccount = `
		SELECT account.id,
		       account.account_key,
		       account.cycle_number,
		       account.usage_count,
		       account.send_limit,
		       account.ramp_day,
		       account.cycle_started_at,
		       account.send_jitter_min_seconds,
		       account.send_jitter_max_seconds,
		       quota.total_limit,
		       quota.remaining_limit
		FROM outreach_email_accounts AS account
		CROSS JOIN LATERAL (
		  SELECT COALESCE(sum(send_limit), 0)::int AS total_limit,
		         COALESCE(sum(
		           CASE
		             WHEN cycle_started_at = $2::timestamptz THEN
		               GREATEST(LEAST(send_limit, ramp_day * 5) - usage_count, 0)
		             ELSE
		               LEAST(
		                 send_limit,
		                 CASE
		                   WHEN cycle_started_at IS NOT NULL
		                    AND usage_count >= LEAST(send_limit, ramp_day * 5)
		                   THEN (ramp_day + 1) * 5
		                   ELSE ramp_day * 5
		                 END
		               )
		           END
		         ), 0)::int AS remaining_limit
		  FROM outreach_email_accounts AS quota_account
		  WHERE quota_account.enabled = true
		    AND quota_account.account_key = ANY($1::text[])
		    AND NOT EXISTS (
		      SELECT 1
		      FROM outreach_email_account_health AS health
		      WHERE health.account_key = quota_account.account_key
		        AND health.last_error = $3
		    )
		) AS quota
		WHERE account.enabled = true
		  AND account.account_key = ANY($1::text[])
		  AND NOT EXISTS (
		    SELECT 1
		    FROM outreach_email_account_health AS health
		    WHERE health.account_key = account.account_key
		      AND health.last_error = $3
		  )
		  AND account.available_at <= clock_timestamp()
		  AND (
		    account.cycle_started_at IS DISTINCT FROM $2::timestamptz
		    OR account.usage_count < LEAST(account.send_limit, account.ramp_day * 5)
		  )
		ORDER BY account.available_at ASC,
		         account.position ASC,
		         account.created_at ASC
		FOR UPDATE OF account
		LIMIT 1`
	var accountID uuid.UUID
	var accountKey string
	var cycleNumber int64
	var usageCount int
	var sendLimit int
	var rampDay int
	var cycleStartedAt *time.Time
	var sendJitterMinSeconds int
	var sendJitterMaxSeconds int
	var totalConfiguredLimit int
	var remainingDailyLimit int
	if err := tx.QueryRow(
		ctx,
		selectAccount,
		accountKeys,
		window.Start,
		emailprovider.AccountUnavailableErrorCode,
	).Scan(
		&accountID,
		&accountKey,
		&cycleNumber,
		&usageCount,
		&sendLimit,
		&rampDay,
		&cycleStartedAt,
		&sendJitterMinSeconds,
		&sendJitterMaxSeconds,
		&totalConfiguredLimit,
		&remainingDailyLimit,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return emailprovider.DeliveryClaim{}, emailprovider.ErrAccountsExhausted
		}
		return emailprovider.DeliveryClaim{}, fmt.Errorf("select available outreach email account: %w", err)
	}

	currentSendLimit := rampedSendLimit(sendLimit, rampDay)
	newDailyCycle := cycleStartedAt == nil || !cycleStartedAt.Equal(window.Start)
	if newDailyCycle && cycleStartedAt != nil {
		cycleNumber++
		if usageCount >= currentSendLimit {
			rampDay = nextEmailRampDay(sendLimit, rampDay)
		}
		usageCount = 0
	}
	if newDailyCycle {
		started := window.Start
		cycleStartedAt = &started
	}
	usageCount++
	dailySendLimit := rampedSendLimit(sendLimit, rampDay)
	reachesLimit := usageCount >= dailySendLimit
	jitter, err := randomPacingJitterForDailyCapacity(
		time.Duration(sendJitterMinSeconds)*time.Second,
		time.Duration(sendJitterMaxSeconds)*time.Second,
		window.End.Sub(window.Start),
		totalConfiguredLimit,
		window.End.Sub(now),
		remainingDailyLimit,
	)
	if err != nil {
		return emailprovider.DeliveryClaim{}, err
	}
	nextGlobalSendAt := now.Add(jitter)
	// Advance by Sydney calendar date rather than adding 24 elapsed hours so the
	// next cycle still begins at the saved Sydney start time across
	// daylight-saving transitions.
	nextAccountSendAt := emailWindowAt(window.End, schedule).Start
	if !reachesLimit {
		schedule, scheduleErr := scheduledDailyAccountSendAt(
			now,
			cycleStartedAt.UTC(),
			window.End,
			usageCount,
			dailySendLimit,
		)
		if scheduleErr != nil {
			return emailprovider.DeliveryClaim{}, scheduleErr
		}
		nextAccountSendAt = schedule
	}

	const consumeSlot = `
		UPDATE outreach_email_accounts
		SET cycle_number = $2,
		    ramp_day = $3,
		    usage_count = $4,
		    cycle_started_at = $5,
		    available_at = $6,
		    last_used_at = $7,
		    updated_at = $7
		WHERE id = $1`
	if _, err := tx.Exec(
		ctx,
		consumeSlot,
		accountID,
		cycleNumber,
		rampDay,
		usageCount,
		cycleStartedAt.UTC(),
		nextAccountSendAt,
		now,
	); err != nil {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("consume outreach email quota: %w", err)
	}
	if _, err := tx.Exec(
		ctx,
		`UPDATE outreach_email_pacing
		 SET next_send_at = $1, last_reserved_at = $2, updated_at = $2
		 WHERE singleton = 1`,
		nextGlobalSendAt,
		now,
	); err != nil {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("advance global outreach email pacing: %w", err)
	}

	const markSending = `
		UPDATE email_campaigns
		SET status = $2, updated_at = now()
		WHERE id = $1 AND status = $3`
	result, err := tx.Exec(
		ctx,
		markSending,
		delivery.CampaignID,
		campaigns.StatusSending,
		campaigns.StatusApproved,
	)
	if err != nil {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("claim approved outreach campaign: %w", err)
	}
	if result.RowsAffected() != 1 {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("%w: campaign was claimed by another sender", campaigns.ErrNotEligible)
	}

	const insertAttempt = `
		INSERT INTO email_delivery_attempts (
			campaign_id, restaurant_id, account_id, bulk_job_id,
			campaign_step, account_cycle, account_sequence, recipient_email,
			status, lease_expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'sending', now() + ($9 * interval '1 second'))
		RETURNING id, send_sequence`
	var claim emailprovider.DeliveryClaim
	if err := tx.QueryRow(
		ctx,
		insertAttempt,
		delivery.CampaignID,
		delivery.RestaurantID,
		accountID,
		delivery.BulkJobID,
		delivery.Step,
		cycleNumber,
		usageCount,
		actualRecipient,
		int64(emailDeliveryLease/time.Second),
	).Scan(&claim.AttemptID, &claim.SendSequence); err != nil {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("create outreach email delivery attempt: %w", err)
	}
	claim.AccountKey = accountKey
	claim.CampaignStep = delivery.Step
	claim.AccountCycle = cycleNumber
	claim.AccountSequence = usageCount

	if err := tx.Commit(ctx); err != nil {
		return emailprovider.DeliveryClaim{}, fmt.Errorf("commit email quota claim: %w", err)
	}
	return claim, nil
}

func randomPacingJitter(minimum, maximum time.Duration) (time.Duration, error) {
	if minimum < 0 || maximum < minimum {
		return 0, fmt.Errorf("invalid outreach email pacing jitter range")
	}
	if minimum%time.Second != 0 || maximum%time.Second != 0 {
		return 0, fmt.Errorf("outreach email pacing jitter must use whole seconds")
	}
	minimumSeconds := int64(minimum / time.Second)
	maximumSeconds := int64(maximum / time.Second)
	if minimumSeconds == maximumSeconds {
		return minimum, nil
	}
	span := maximumSeconds - minimumSeconds + 1
	offset, err := cryptorand.Int(cryptorand.Reader, big.NewInt(span))
	if err != nil {
		return 0, fmt.Errorf("sample outreach email pacing jitter: %w", err)
	}
	return time.Duration(minimumSeconds+offset.Int64()) * time.Second, nil
}

func randomPacingJitterForDailyCapacity(
	minimum time.Duration,
	maximum time.Duration,
	window time.Duration,
	totalDailyLimit int,
	remainingWindow time.Duration,
	remainingDailyLimit int,
) (time.Duration, error) {
	if window <= 0 || totalDailyLimit < 1 || remainingWindow <= 0 || remainingDailyLimit < 1 {
		return 0, fmt.Errorf("invalid scheduled outreach email daily capacity")
	}
	capacityMaximum := window / time.Duration(totalDailyLimit)
	if remainingDailyLimit > 1 {
		remainingMaximum := remainingWindow / time.Duration(remainingDailyLimit-1)
		if remainingMaximum < capacityMaximum {
			capacityMaximum = remainingMaximum
		}
	}
	capacityMaximum = capacityMaximum.Truncate(time.Second)
	if capacityMaximum < time.Second {
		return 0, fmt.Errorf("configured outreach email daily quota cannot fit within the saved Australia/Sydney send window")
	}
	// The configured jitter protects one mailbox. When several mailboxes are
	// interleaved, their individual slot anchors preserve that cadence, so the
	// aggregate gate may safely compress below the per-mailbox minimum. A random
	// half-capacity lower bound avoids bursts while leaving enough headroom for
	// the full daily allowance to finish before the saved window closes.
	if capacityMaximum < minimum {
		minimum = (capacityMaximum / 2).Truncate(time.Second)
		if minimum < time.Second {
			minimum = time.Second
		}
	}
	if maximum > capacityMaximum {
		maximum = capacityMaximum
	}
	if maximum < minimum {
		maximum = minimum
	}
	return randomPacingJitter(minimum, maximum)
}

func scheduledDailyAccountSendAt(
	now time.Time,
	windowStart time.Time,
	windowEnd time.Time,
	usedSlots int,
	sendLimit int,
) (time.Time, error) {
	if usedSlots < 1 || sendLimit < 1 || usedSlots >= sendLimit {
		return time.Time{}, fmt.Errorf("invalid outreach email account slot")
	}
	if !windowEnd.After(windowStart) {
		return time.Time{}, fmt.Errorf("invalid scheduled outreach email pacing policy")
	}
	if now.Before(windowStart) || !now.Before(windowEnd) {
		return time.Time{}, fmt.Errorf("scheduled outreach email is outside the Sydney send window")
	}
	slotWidth := windowEnd.Sub(windowStart) / time.Duration(sendLimit)

	// Use fixed anchors when the worker is on time. If it starts late or is
	// delayed, divide the remaining time across the remaining sends. The global
	// jitter gate still supplies the minimum inter-message delay, but missed
	// anchors are not pushed beyond the saved end time.
	slotAt := windowStart.Add(time.Duration(usedSlots) * slotWidth)
	if !slotAt.After(now) {
		remainingSlots := sendLimit - usedSlots
		catchUpWidth := windowEnd.Sub(now) / time.Duration(remainingSlots+1)
		slotAt = now.Add(catchUpWidth)
	}
	if !slotAt.Before(windowEnd) {
		return time.Time{}, fmt.Errorf("remaining outreach email quota cannot fit before the Sydney send window closes")
	}
	return slotAt, nil
}

// ReconcileStaleEmailDeliveries fails closed when a worker disappears after
// claiming quota but before recording the provider outcome. The slot remains
// consumed and the campaign requires an operator decision; automatic retry
// could otherwise duplicate a message accepted by the provider.
func (repo *Postgres) ReconcileStaleEmailDeliveries(ctx context.Context) (int, error) {
	if repo.pool == nil {
		return 0, fmt.Errorf("database pool is not configured")
	}

	rows, err := repo.pool.Query(ctx, `
		SELECT id
		FROM email_delivery_attempts
		WHERE status = 'sending' AND lease_expires_at <= now()
		ORDER BY lease_expires_at, id
		LIMIT 100`)
	if err != nil {
		return 0, fmt.Errorf("list stale outreach email deliveries: %w", err)
	}
	var attemptIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan stale outreach email delivery: %w", err)
		}
		attemptIDs = append(attemptIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate stale outreach email deliveries: %w", err)
	}
	rows.Close()

	count := 0
	for _, attemptID := range attemptIDs {
		err := repo.MarkEmailDeliveryUnknown(
			ctx,
			emailprovider.DeliveryClaim{AttemptID: attemptID},
			"delivery_lease_expired",
		)
		if errors.Is(err, errDeliveryAttemptNotSending) {
			continue
		}
		if err != nil {
			return count, fmt.Errorf("reconcile stale outreach email delivery %s: %w", attemptID, err)
		}
		count++
	}
	return count, nil
}

func (repo *Postgres) NextEmailAccountAvailableAt(
	ctx context.Context,
	accountKeys []string,
) (*time.Time, error) {
	if repo.pool == nil {
		return nil, fmt.Errorf("database pool is not configured")
	}
	if len(accountKeys) == 0 {
		return nil, nil
	}

	const query = `
		WITH configured AS MATERIALIZED (
		  SELECT id, position, created_at, usage_count, send_limit, ramp_day, available_at
		  FROM outreach_email_accounts AS account
		  WHERE account.enabled = true
		    AND account.account_key = ANY($1::text[])
		    AND NOT EXISTS (
		      SELECT 1
		      FROM outreach_email_account_health AS health
		      WHERE health.account_key = account.account_key
		        AND health.last_error = $2
		    )
		)
		SELECT (SELECT count(*) FROM configured),
		       GREATEST((SELECT min(available_at) FROM configured), pacing.next_send_at),
		       clock_timestamp()
		FROM outreach_email_pacing AS pacing
		WHERE pacing.singleton = 1`
	var accountCount int
	var next *time.Time
	var databaseNow time.Time
	if err := repo.pool.QueryRow(
		ctx,
		query,
		accountKeys,
		emailprovider.AccountUnavailableErrorCode,
	).Scan(&accountCount, &next, &databaseNow); err != nil {
		return nil, fmt.Errorf("get next outreach email account availability: %w", err)
	}
	if accountCount == 0 {
		return nil, fmt.Errorf("no configured outreach email accounts are enabled")
	}
	if next == nil {
		return nil, nil
	}
	schedule, err := loadEmailSendSchedule(ctx, repo.pool, false)
	if err != nil {
		return nil, err
	}
	adjusted := nextScheduledSendAt(databaseNow.UTC(), next.UTC(), schedule)
	if !adjusted.After(databaseNow) {
		return nil, nil
	}
	return &adjusted, nil
}

func (repo *Postgres) CompleteEmailDelivery(
	ctx context.Context,
	claim emailprovider.DeliveryClaim,
	providerMessageID string,
) error {
	return repo.finalizeEmailDelivery(ctx, claim, deliveryFinalization{
		status:            "sent",
		eventType:         campaigns.EventSent,
		providerMessageID: strings.TrimSpace(providerMessageID),
	})
}

func (repo *Postgres) SkipEmailDelivery(
	ctx context.Context,
	claim emailprovider.DeliveryClaim,
	skipped bool,
	redirected bool,
) error {
	return repo.finalizeEmailDelivery(ctx, claim, deliveryFinalization{
		status:     "skipped",
		eventType:  campaigns.EventSkipped,
		skipped:    skipped,
		redirected: redirected,
	})
}

func (repo *Postgres) MarkEmailDeliveryUnknown(
	ctx context.Context,
	claim emailprovider.DeliveryClaim,
	errorCode string,
) error {
	code := strings.TrimSpace(errorCode)
	if code == "" {
		code = "provider_send_unknown"
	}
	return repo.finalizeEmailDelivery(ctx, claim, deliveryFinalization{
		status:    "unknown",
		eventType: campaigns.EventFailed,
		errorCode: code,
	})
}

func (repo *Postgres) FailEmailDelivery(
	ctx context.Context,
	claim emailprovider.DeliveryClaim,
	errorCode string,
) error {
	// The finalizer schedules only the explicit definitive-rejection allowlist.
	// Every other failure is retained for audit and stops the campaign rather
	// than silently re-entering the eligible queue.
	code := strings.TrimSpace(errorCode)
	if code == "" {
		code = "provider_rejected_before_acceptance"
	}
	return repo.finalizeEmailDelivery(ctx, claim, deliveryFinalization{
		status:    "failed",
		eventType: campaigns.EventFailed,
		errorCode: code,
	})
}

type deliveryFinalization struct {
	status            string
	eventType         string
	providerMessageID string
	errorCode         string
	skipped           bool
	redirected        bool
}

const countEmailDeliveryAttemptsForStepQuery = `
	SELECT count(*) FILTER (WHERE status <> 'skipped')::int,
	       COALESCE(bool_or(
	         status IN ('sent', 'sending', 'unknown')
	         OR (
	           status = 'failed'
	           AND lower(trim(error_code)) NOT IN (` + retryableEmailFailureCodesSQL + `)
	         )
	       ), false),
	       clock_timestamp()
	FROM email_delivery_attempts
	WHERE campaign_id = $1 AND campaign_step = $2`

const coolRetryableEmailAccountQuery = `
	UPDATE outreach_email_accounts
	SET available_at = GREATEST(available_at, $2),
	    updated_at = now()
	WHERE id = $1
	RETURNING available_at`

// resetRetryableEmailBulkJobAttemptsQuery closes the crash window between
// recording a definitive provider rejection and the handler's normal durable
// deferral. The attempt's bulk_job_id identifies only the job that crossed the
// provider boundary. Resetting its persisted attempt count makes the running
// row reclaimable after a process crash without disrupting the live handler's
// lease heartbeat or overriding an administrator's cancellation.
const resetRetryableEmailBulkJobAttemptsQuery = `
	UPDATE job_runs
	SET attempts = 0,
	    updated_at = now()
	WHERE id = $1
	  AND job_type = $2
	  AND status = 'running'`

const lockFailedEmailCampaignQuery = `
	SELECT status, current_step, next_step, next_send_at
	FROM email_campaigns
	WHERE id = $1
	FOR UPDATE`

const scheduleRetryableEmailCampaignQuery = `
	UPDATE email_campaigns
	SET status = $4,
	    next_send_at = GREATEST(next_send_at, $5),
	    stopped_reason = '',
	    updated_at = now()
	WHERE id = $1
	  AND current_step < $2
	  AND next_step = $2
	  AND status = $3`

const stopExhaustedEmailCampaignQuery = `
	UPDATE email_campaigns
	SET status = $4,
	    stopped_reason = $5,
	    updated_at = now()
	WHERE id = $1
	  AND current_step < $2
	  AND next_step = $2
	  AND status = $3`

const stopNonRetryableEmailCampaignQuery = `
	UPDATE email_campaigns
	SET status = $4,
	    stopped_reason = $5,
	    updated_at = now()
	WHERE id = $1
	  AND current_step < $2
	  AND next_step = $2
	  AND status = $3`

const retainStoppedEmailCampaignQuery = `
	UPDATE email_campaigns
	SET updated_at = now()
	WHERE id = $1
	  AND current_step < $2
	  AND next_step = $2
	  AND status = $3`

const finalizeEmailDeliveryAttemptQuery = `
	UPDATE email_delivery_attempts
	SET status = $2,
	    provider_message_id = $3,
	    error_code = $4,
	    sent_at = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END,
	    lease_expires_at = NULL,
	    updated_at = now()
	WHERE id = $1
	  AND status = 'sending'
	  AND ($2 = 'unknown' OR lease_expires_at > now())
	RETURNING campaign_id, restaurant_id, account_id, bulk_job_id, send_sequence,
	          account_cycle, account_sequence, recipient_email, campaign_step`

func (repo *Postgres) finalizeEmailDelivery(
	ctx context.Context,
	claim emailprovider.DeliveryClaim,
	finalization deliveryFinalization,
) error {
	if repo.pool == nil {
		return fmt.Errorf("database pool is not configured")
	}
	if claim.AttemptID == uuid.Nil {
		return fmt.Errorf("delivery attempt is required")
	}

	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin email delivery finalization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var lockedRestaurantID uuid.UUID
	if err := tx.QueryRow(
		ctx,
		`SELECT restaurant_id FROM email_delivery_attempts WHERE id = $1`,
		claim.AttemptID,
	).Scan(&lockedRestaurantID); errors.Is(err, pgx.ErrNoRows) {
		return errDeliveryAttemptNotSending
	} else if err != nil {
		return fmt.Errorf("load delivery attempt restaurant: %w", err)
	}
	if err := platformdb.LockRestaurantWorkflow(ctx, tx, lockedRestaurantID); err != nil {
		return err
	}

	var campaignID uuid.UUID
	var restaurantID uuid.UUID
	var accountID uuid.UUID
	var bulkJobID *uuid.UUID
	var sendSequence int64
	var accountCycle int64
	var accountSequence int
	var recipientEmail string
	var campaignStep int
	err = tx.QueryRow(
		ctx,
		finalizeEmailDeliveryAttemptQuery,
		claim.AttemptID,
		finalization.status,
		finalization.providerMessageID,
		finalization.errorCode,
	).Scan(
		&campaignID,
		&restaurantID,
		&accountID,
		&bulkJobID,
		&sendSequence,
		&accountCycle,
		&accountSequence,
		&recipientEmail,
		&campaignStep,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingStatus string
		if statusErr := tx.QueryRow(ctx, `SELECT status FROM email_delivery_attempts WHERE id = $1`, claim.AttemptID).Scan(&existingStatus); statusErr == nil && existingStatus == finalization.status {
			return nil
		}
		return errDeliveryAttemptNotSending
	}
	if err != nil {
		return fmt.Errorf("update email delivery attempt: %w", err)
	}

	var retryDecision emailFailureRetryDecision
	var totalStepAttempts int
	var blockingStepOutcome bool
	var retryAt *time.Time
	var retryNotBefore *time.Time
	var accountCooldownUntil *time.Time
	var retryableFailure bool
	var failedCampaignStatus string
	if finalization.status == "failed" {
		var currentStep int
		var nextStep *int
		var campaignNextSendAt *time.Time
		if err := tx.QueryRow(ctx, lockFailedEmailCampaignQuery, campaignID).Scan(
			&failedCampaignStatus,
			&currentStep,
			&nextStep,
			&campaignNextSendAt,
		); errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("outreach campaign is unavailable while recording failure")
		} else if err != nil {
			return fmt.Errorf("lock failed outreach campaign: %w", err)
		}
		if (failedCampaignStatus != campaigns.StatusSending && failedCampaignStatus != campaigns.StatusStopped) ||
			nextStep == nil || *nextStep != campaignStep || campaignNextSendAt == nil || currentStep >= campaignStep {
			return fmt.Errorf("outreach campaign changed before delivery failure finalization")
		}

		// Avoid a count/schedule lookup for non-allowlisted failures. A count of
		// one is enough to ask the policy whether this code can ever retry.
		retryableFailure = decideEmailFailureRetry(finalization.status, finalization.errorCode, 1).Retryable
		if retryableFailure {
			var databaseNow time.Time
			if err := tx.QueryRow(
				ctx,
				countEmailDeliveryAttemptsForStepQuery,
				campaignID,
				campaignStep,
			).Scan(&totalStepAttempts, &blockingStepOutcome, &databaseNow); err != nil {
				return fmt.Errorf("count outreach email delivery attempts for step: %w", err)
			}
			if totalStepAttempts < 1 {
				return fmt.Errorf("failed outreach email delivery attempt was not counted")
			}
			if !blockingStepOutcome {
				retryDecision = decideEmailFailureRetry(
					finalization.status,
					finalization.errorCode,
					totalStepAttempts,
				)
			}
			schedule, err := loadEmailSendSchedule(ctx, tx, false)
			if err != nil {
				return err
			}
			nextRetryAt := nextEmailDeliveryRetryAt(databaseNow.UTC(), schedule)
			retryAt = &nextRetryAt
			actualRetryAt := nextRetryAt
			if campaignNextSendAt.After(actualRetryAt) {
				actualRetryAt = campaignNextSendAt.UTC()
			}
			retryNotBefore = &actualRetryAt
			var actualAccountAvailableAt time.Time
			if err := tx.QueryRow(ctx, coolRetryableEmailAccountQuery, accountID, nextRetryAt).Scan(&actualAccountAvailableAt); err != nil {
				return fmt.Errorf("cool retryable outreach email account: %w", err)
			}
			actualAccountAvailableAt = actualAccountAvailableAt.UTC()
			accountCooldownUntil = &actualAccountAvailableAt
		}
	}

	retryScheduled := finalization.status == "failed" &&
		failedCampaignStatus != campaigns.StatusStopped && retryDecision.Schedule
	retryExhausted := finalization.status == "failed" &&
		failedCampaignStatus != campaigns.StatusStopped && retryDecision.Exhausted

	eventMetadata := map[string]any{
		"step":                campaignStep,
		"provider_message":    finalization.providerMessageID,
		"bulk_outreach":       true,
		"account_key":         claim.AccountKey,
		"account_cycle":       accountCycle,
		"account_sequence":    accountSequence,
		"send_sequence":       sendSequence,
		"skipped":             finalization.skipped,
		"redirected":          finalization.redirected,
		"error_code":          finalization.errorCode,
		"delivery_attempt_id": claim.AttemptID,
	}
	if retryableFailure {
		eventMetadata["attempt_number"] = totalStepAttempts
		eventMetadata["maximum_attempts"] = maxEmailDeliveryAttemptsPerStep
		eventMetadata["account_cooldown_until"] = accountCooldownUntil
		if retryScheduled {
			eventMetadata["retry_not_before"] = retryNotBefore
		}
		eventMetadata["retry_scheduled"] = retryScheduled
		eventMetadata["retry_exhausted"] = retryExhausted
	}
	metadata, err := json.Marshal(eventMetadata)
	if err != nil {
		return fmt.Errorf("encode email delivery event: %w", err)
	}

	const insertEvent = `
		INSERT INTO email_events (
			campaign_id, restaurant_id, event_type, metadata, delivery_attempt_id
		) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (delivery_attempt_id, event_type)
		WHERE delivery_attempt_id IS NOT NULL
		DO NOTHING`
	if _, err := tx.Exec(ctx, insertEvent, campaignID, restaurantID, finalization.eventType, metadata, claim.AttemptID); err != nil {
		return fmt.Errorf("insert email delivery event: %w", err)
	}

	var result pgconn.CommandTag
	if finalization.status == "sent" {
		const advanceCampaign = `
			WITH next_enabled AS (
			  SELECT step.position, step.delay_hours
			  FROM email_campaigns current_campaign
			  JOIN outreach_email_sequence_steps step
			    ON step.sequence_id = current_campaign.sequence_id
			  WHERE current_campaign.id = $1
			    AND step.enabled = true
			    AND step.position > $2
			  ORDER BY step.position
			  LIMIT 1
			)
			UPDATE email_campaigns campaign
			SET current_step = $2,
			    next_step = CASE
			      WHEN campaign.status = $4 THEN NULL
			      ELSE (SELECT position FROM next_enabled)
			    END,
			    next_send_at = CASE
			      WHEN campaign.status = $4 OR NOT EXISTS (SELECT 1 FROM next_enabled) THEN NULL
			      ELSE now() + ((SELECT delay_hours FROM next_enabled) * interval '1 hour')
			    END,
			    completed_at = CASE
			      WHEN campaign.status = $4 OR NOT EXISTS (SELECT 1 FROM next_enabled) THEN now()
			      ELSE NULL
			    END,
			    status = CASE
			      WHEN campaign.status = $4 THEN $4
			      WHEN EXISTS (SELECT 1 FROM next_enabled) THEN $5
			      ELSE $6
			    END,
			    last_sent_at = now(),
			    updated_at = now()
			WHERE campaign.id = $1
			  AND campaign.current_step < $2
			  AND campaign.status IN ($3, $4)`
		result, err = tx.Exec(
			ctx, advanceCampaign, campaignID, campaignStep,
			campaigns.StatusSending, campaigns.StatusStopped,
			campaigns.StatusApproved, campaigns.StatusSent,
		)
	} else if finalization.status == "failed" {
		switch {
		case failedCampaignStatus == campaigns.StatusStopped:
			result, err = tx.Exec(
				ctx,
				retainStoppedEmailCampaignQuery,
				campaignID,
				campaignStep,
				campaigns.StatusStopped,
			)
		case retryScheduled:
			result, err = tx.Exec(
				ctx,
				scheduleRetryableEmailCampaignQuery,
				campaignID,
				campaignStep,
				campaigns.StatusSending,
				campaigns.StatusApproved,
				*retryAt,
			)
		case retryExhausted:
			result, err = tx.Exec(
				ctx,
				stopExhaustedEmailCampaignQuery,
				campaignID,
				campaignStep,
				campaigns.StatusSending,
				campaigns.StatusStopped,
				emailRetryExhaustedReason,
			)
		default:
			stoppedReason := emailFailureNotRetryableReason
			if blockingStepOutcome {
				stoppedReason = emailFailureOutcomeConflict
			}
			result, err = tx.Exec(
				ctx,
				stopNonRetryableEmailCampaignQuery,
				campaignID,
				campaignStep,
				campaigns.StatusSending,
				campaigns.StatusStopped,
				stoppedReason,
			)
		}
	} else {
		nextStatus := campaigns.StatusApproved
		if finalization.status == "unknown" {
			nextStatus = campaigns.StatusSendUnknown
		}
		const retainCampaignProgress = `
			UPDATE email_campaigns
			SET status = CASE WHEN status = $3 THEN $3 ELSE $2 END,
			    updated_at = now()
			WHERE id = $1
			  AND status IN ($4, $3)`
		result, err = tx.Exec(
			ctx, retainCampaignProgress, campaignID, nextStatus,
			campaigns.StatusStopped, campaigns.StatusSending,
		)
	}
	if err != nil {
		return fmt.Errorf("finalize outreach campaign: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("outreach campaign changed before delivery finalization")
	}

	if retryScheduled || retryExhausted {
		retryEventType := emailRetryScheduledEvent
		if retryExhausted {
			retryEventType = emailRetryExhaustedEvent
		}
		if _, err := tx.Exec(
			ctx,
			insertEvent,
			campaignID,
			restaurantID,
			retryEventType,
			metadata,
			claim.AttemptID,
		); err != nil {
			return fmt.Errorf("insert email delivery retry event: %w", err)
		}
	}

	// A retryable code proves that this attempt failed before provider
	// acceptance. Reset the exact running bulk job's persisted attempt count in
	// the same transaction as the attempt, campaign, mailbox cooldown, and audit
	// records. The live handler keeps its lease and performs the normal fenced
	// deferral. If the process dies first, the expired running row is reclaimable
	// instead of becoming stranded at its one-attempt worker limit.
	if finalization.status == "failed" && retryAt != nil && bulkJobID != nil {
		result, err := tx.Exec(
			ctx,
			resetRetryableEmailBulkJobAttemptsQuery,
			*bulkJobID,
			BulkSendJobType,
		)
		if err != nil {
			return fmt.Errorf("make retryable outreach bulk job reclaimable: %w", err)
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("retryable outreach bulk job is not running and cannot be made reclaimable")
		}
	}

	if finalization.status == "sent" {
		const updateRestaurant = `
			UPDATE restaurants
			SET is_contacted = true,
			    email_sent = true,
			    email_send_count = email_send_count + 1,
			    last_email_sent_at = now(),
			    last_email_send_sequence = $2,
			    last_email_recipient = $3,
			    status = CASE WHEN status IN ('lead', 'demo_ready') THEN 'emailed' ELSE status END,
			    updated_at = now()
			WHERE id = $1`
		result, err := tx.Exec(ctx, updateRestaurant, restaurantID, sendSequence, recipientEmail)
		if err != nil {
			return fmt.Errorf("record confirmed restaurant email: %w", err)
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("restaurant was not found while recording confirmed email")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit email delivery finalization: %w", err)
	}
	return nil
}

var _ emailprovider.QuotaStore = (*Postgres)(nil)
var _ emailprovider.AccountFailureStore = (*Postgres)(nil)
