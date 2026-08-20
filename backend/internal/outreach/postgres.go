package outreach

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/campaigns"
	repository "github.com/rajchodisetti/restaurant-platform/backend/internal/platform/errors"
)

type Repository interface {
	ListEligibleLeads(ctx context.Context, limit int) ([]EligibleLead, error)
	CountEligibleLeads(ctx context.Context) (int, error)
}

type Postgres struct {
	pool *pgxpool.Pool
}

const ownerFirstNameSelectExpression = `COALESCE(
  NULLIF(trim(profile.apollo_lead #>> '{contact,first_name}'), ''),
  NULLIF(split_part(trim(profile.apollo_lead #>> '{contact,name}'), ' ', 1), ''),
  NULLIF(split_part(trim(profile.owners ->> 0), ' ', 1), ''),
  ''
)`

func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

func GetEmailJobControl(ctx context.Context, pool *pgxpool.Pool) (EmailJobControl, error) {
	if pool == nil {
		return EmailJobControl{}, nil
	}
	const query = `
		SELECT enabled, enabled_at, enabled_by, updated_at
		FROM outreach_runtime_control
		WHERE control_key = 'email_job'`
	var control EmailJobControl
	err := pool.QueryRow(ctx, query).Scan(
		&control.Enabled,
		&control.EnabledAt,
		&control.EnabledBy,
		&control.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return EmailJobControl{}, nil
	}
	if err != nil {
		return EmailJobControl{}, fmt.Errorf("get outreach email job control: %w", err)
	}
	return control, nil
}

func SetEmailJobControl(ctx context.Context, pool *pgxpool.Pool, enabled bool, enabledBy *uuid.UUID) (EmailJobControl, error) {
	if pool == nil {
		return EmailJobControl{}, fmt.Errorf("database pool is not configured")
	}
	const query = `
		INSERT INTO outreach_runtime_control (control_key, enabled, enabled_at, enabled_by, updated_at)
		VALUES ('email_job', $1, CASE WHEN $1 THEN now() ELSE NULL END, CASE WHEN $1 THEN $2 ELSE NULL END, now())
		ON CONFLICT (control_key) DO UPDATE
		SET enabled = EXCLUDED.enabled,
		    enabled_at = EXCLUDED.enabled_at,
		    enabled_by = EXCLUDED.enabled_by,
		    updated_at = now()
		RETURNING enabled, enabled_at, enabled_by, updated_at`
	var control EmailJobControl
	if err := pool.QueryRow(ctx, query, enabled, enabledBy).Scan(
		&control.Enabled,
		&control.EnabledAt,
		&control.EnabledBy,
		&control.UpdatedAt,
	); err != nil {
		return EmailJobControl{}, fmt.Errorf("set outreach email job control: %w", err)
	}
	return control, nil
}

func DisableEmailJobControl(ctx context.Context, pool *pgxpool.Pool) (EmailJobControl, error) {
	if pool == nil {
		return EmailJobControl{}, fmt.Errorf("database pool is not configured")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return EmailJobControl{}, fmt.Errorf("begin disabling outreach email job: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var control EmailJobControl
	if err := tx.QueryRow(ctx, `
		INSERT INTO outreach_runtime_control (control_key, enabled, enabled_at, enabled_by, updated_at)
		VALUES ('email_job', false, NULL, NULL, now())
		ON CONFLICT (control_key) DO UPDATE
		SET enabled = false, enabled_at = NULL, enabled_by = NULL, updated_at = now()
		RETURNING enabled, enabled_at, enabled_by, updated_at`).Scan(
		&control.Enabled, &control.EnabledAt, &control.EnabledBy, &control.UpdatedAt,
	); err != nil {
		return EmailJobControl{}, fmt.Errorf("disable outreach email job control: %w", err)
	}
	// Deferred work has not crossed a provider boundary, so it is safe to
	// cancel. A running lease is left intact to finalize any in-flight provider
	// outcome; RunBulkSend rechecks the disabled control before another send.
	if _, err := tx.Exec(ctx, `
		UPDATE job_runs
		SET status = 'cancelled',
		    locked_at = NULL,
		    locked_by = NULL,
		    lease_expires_at = NULL,
		    last_error = 'Cancelled when outreach email job was disabled.',
		    updated_at = now()
		WHERE job_type = $1 AND status = 'queued'`, BulkSendJobType); err != nil {
		return EmailJobControl{}, fmt.Errorf("cancel deferred outreach email job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return EmailJobControl{}, fmt.Errorf("commit disabling outreach email job: %w", err)
	}
	return control, nil
}

func EnableEmailJobControl(
	ctx context.Context,
	pool *pgxpool.Pool,
	enabledBy uuid.UUID,
) (EmailJobControl, string, error) {
	if pool == nil {
		return EmailJobControl{}, "", fmt.Errorf("database pool is not configured")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return EmailJobControl{}, "", fmt.Errorf("begin enabling outreach email job: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var control EmailJobControl
	if err := tx.QueryRow(ctx, `
		INSERT INTO outreach_runtime_control (control_key, enabled, enabled_at, enabled_by, updated_at)
		VALUES ('email_job', true, now(), $1, now())
		ON CONFLICT (control_key) DO UPDATE
		SET enabled = true, enabled_at = now(), enabled_by = EXCLUDED.enabled_by, updated_at = now()
		RETURNING enabled, enabled_at, enabled_by, updated_at`, enabledBy).Scan(
		&control.Enabled, &control.EnabledAt, &control.EnabledBy, &control.UpdatedAt,
	); err != nil {
		return EmailJobControl{}, "", fmt.Errorf("enable outreach email job control: %w", err)
	}
	var activeJobID string
	err = tx.QueryRow(ctx, `
		SELECT id::text
		FROM job_runs
		WHERE job_type = $1 AND status IN ('queued', 'running')
		ORDER BY created_at
		LIMIT 1
		FOR UPDATE`, BulkSendJobType).Scan(&activeJobID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return EmailJobControl{}, "", fmt.Errorf("lock active outreach job while enabling: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return EmailJobControl{}, "", fmt.Errorf("commit enabling outreach email job: %w", err)
	}
	return control, activeJobID, nil
}

const eligibleLeadsBaseQuery = `
	FROM email_campaigns campaign
	JOIN restaurants restaurant ON restaurant.id = campaign.restaurant_id
	JOIN outreach_email_sequences sequence ON sequence.id = campaign.sequence_id
	JOIN outreach_email_sequence_steps step
	  ON step.sequence_id = campaign.sequence_id
	 AND step.position = campaign.next_step
	WHERE campaign.campaign_type = 'outreach'
	  AND campaign.sequence_id IS NOT NULL
	  AND campaign.status = 'approved'
	  AND campaign.next_step IS NOT NULL
	  AND campaign.next_send_at IS NOT NULL
	  AND campaign.next_send_at <= now()
	  AND sequence.status IN ('approved', 'archived')
	  AND sequence.approved_at IS NOT NULL
	  AND step.enabled = true
	  AND NOT EXISTS (
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
	  )
	  AND (
	    SELECT count(*)
	    FROM email_delivery_attempts prior_attempt
	    WHERE prior_attempt.campaign_id = campaign.id
	      AND prior_attempt.campaign_step = campaign.next_step
	      AND prior_attempt.status <> 'skipped'
	  ) < 3
	  AND length(trim(restaurant.name)) > 0
	  AND lower(trim(restaurant.email)) ~ '^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$'
	  AND (
	    SELECT count(*)
	    FROM restaurants shared_restaurant
	    WHERE lower(trim(shared_restaurant.email)) = lower(trim(restaurant.email))
	  ) <= 3
	  AND restaurant.status IN ('lead', 'emailed')
	  AND restaurant.shown_interest = false
	  AND restaurant.outreach_consent_basis = 'inferred_business'
	  AND restaurant.outreach_consent_recorded_at IS NOT NULL
	  AND length(trim(restaurant.outreach_consent_source)) > 0
	  AND jsonb_typeof(restaurant.outreach_consent_evidence) = 'object'
	  AND restaurant.outreach_consent_evidence <> '{}'::jsonb`

const eligibleLeadsOrderBy = `
		ORDER BY (campaign.current_step > 0) DESC,
		         campaign.next_send_at ASC,
		         restaurant.created_at ASC`

func (repo *Postgres) ListEligibleLeads(ctx context.Context, limit int) ([]EligibleLead, error) {
	if repo.pool == nil {
		return nil, fmt.Errorf("database pool is not configured")
	}
	if limit < 1 {
		return nil, fmt.Errorf("limit must be at least 1")
	}

	query := `
		SELECT campaign.id, restaurant.id, COALESCE(campaign.demo_site_id, '00000000-0000-0000-0000-000000000000'::uuid), campaign.next_step` + eligibleLeadsBaseQuery + eligibleLeadsOrderBy + `
		LIMIT $1`

	rows, err := repo.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("list eligible leads: %w", err)
	}
	defer rows.Close()

	var leads []EligibleLead
	for rows.Next() {
		var lead EligibleLead
		if err := rows.Scan(&lead.CampaignID, &lead.RestaurantID, &lead.DemoSiteID, &lead.Step); err != nil {
			return nil, fmt.Errorf("scan eligible lead: %w", err)
		}
		leads = append(leads, lead)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate eligible leads: %w", err)
	}
	return leads, nil
}

type sequenceDeliveryRepository interface {
	GetSequenceDelivery(ctx context.Context, campaignID uuid.UUID, step int) (SequenceDelivery, error)
	PrepareSequenceDelivery(ctx context.Context, campaignID uuid.UUID, step int, subject, bodyHTML, bodyText string) error
	FinalizeSequenceDelivery(ctx context.Context, finalization SequenceDeliveryFinalization) error
	NextSequenceDueAt(ctx context.Context) (*time.Time, error)
}

func (repo *Postgres) ListSequenceSteps(ctx context.Context, sequenceID uuid.UUID) ([]SequenceStep, error) {
	if repo.pool == nil {
		return nil, fmt.Errorf("database pool is not configured")
	}
	rows, err := repo.pool.Query(ctx, `
		SELECT id, sequence_id, position, enabled, delay_hours,
		       subject_template, body_text_template, created_at, updated_at
		FROM outreach_email_sequence_steps
		WHERE sequence_id = $1
		ORDER BY position`, sequenceID)
	if err != nil {
		return nil, fmt.Errorf("list outreach sequence steps: %w", err)
	}
	defer rows.Close()
	steps := []SequenceStep{}
	for rows.Next() {
		var step SequenceStep
		if err := rows.Scan(
			&step.ID, &step.SequenceID, &step.Position, &step.Enabled,
			&step.DelayHours, &step.SubjectTemplate, &step.BodyTextTemplate,
			&step.CreatedAt, &step.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan outreach sequence step: %w", err)
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outreach sequence steps: %w", err)
	}
	if len(steps) == 0 {
		var exists bool
		if err := repo.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM outreach_email_sequences WHERE id = $1)`, sequenceID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check outreach sequence: %w", err)
		}
		if !exists {
			return nil, repository.ErrNotFound
		}
	}
	return steps, nil
}

const activeSequenceSignatureQuery = `
	SELECT signature_name, signature_title, signature_details
	FROM outreach_email_sequences
	WHERE is_active = true
	  AND status = 'approved'
	  AND approved_at IS NOT NULL
	LIMIT 1`

func (repo *Postgres) GetActiveSequenceSignature(ctx context.Context) (SequenceSignature, error) {
	if repo.pool == nil {
		return SequenceSignature{}, fmt.Errorf("database pool is not configured")
	}
	var signature SequenceSignature
	if err := repo.pool.QueryRow(ctx, activeSequenceSignatureQuery).Scan(
		&signature.Name, &signature.Title, &signature.AdditionalDetails,
	); errors.Is(err, pgx.ErrNoRows) {
		return SequenceSignature{}, repository.ErrNotFound
	} else if err != nil {
		return SequenceSignature{}, fmt.Errorf("query active outreach sequence signature: %w", err)
	}
	return signature, nil
}

func (repo *Postgres) GetSequenceSignature(ctx context.Context, sequenceID uuid.UUID) (SequenceSignature, error) {
	if repo.pool == nil {
		return SequenceSignature{}, fmt.Errorf("database pool is not configured")
	}
	var signature SequenceSignature
	if err := repo.pool.QueryRow(ctx, `
		SELECT signature_name, signature_title, signature_details
		FROM outreach_email_sequences
		WHERE id = $1`, sequenceID).Scan(
		&signature.Name, &signature.Title, &signature.AdditionalDetails,
	); errors.Is(err, pgx.ErrNoRows) {
		return SequenceSignature{}, repository.ErrNotFound
	} else if err != nil {
		return SequenceSignature{}, fmt.Errorf("load outreach sequence signature: %w", err)
	}
	return signature, nil
}

func (repo *Postgres) GetGreetingFacts(ctx context.Context, restaurantID uuid.UUID) (GreetingFacts, error) {
	if repo.pool == nil {
		return GreetingFacts{}, fmt.Errorf("database pool is not configured")
	}
	const query = `
		SELECT restaurant.name,
		       ` + ownerFirstNameSelectExpression + `,
		       COALESCE(profile.google_place_id, ''),
		       COALESCE(profile.scrape_status, ''),
		       COALESCE(profile.city, ''),
		       COALESCE(profile.cuisines, '[]'::jsonb),
		       profile.rating,
		       profile.reviews_count
		FROM restaurants restaurant
		LEFT JOIN restaurant_profiles profile ON profile.restaurant_id = restaurant.id
		WHERE restaurant.id = $1`
	var facts GreetingFacts
	if err := repo.pool.QueryRow(ctx, query, restaurantID).Scan(
		&facts.RestaurantName,
		&facts.OwnerFirstName,
		&facts.GooglePlaceID,
		&facts.ScrapeStatus,
		&facts.City,
		&facts.Cuisines,
		&facts.Rating,
		&facts.ReviewCount,
	); errors.Is(err, pgx.ErrNoRows) {
		return GreetingFacts{}, repository.ErrNotFound
	} else if err != nil {
		return GreetingFacts{}, fmt.Errorf("load restaurant greeting facts: %w", err)
	}
	return facts, nil
}

func (repo *Postgres) GetSequenceDelivery(ctx context.Context, campaignID uuid.UUID, position int) (SequenceDelivery, error) {
	if repo.pool == nil {
		return SequenceDelivery{}, fmt.Errorf("database pool is not configured")
	}
	const query = `
		SELECT campaign.id,
		       restaurant.id,
		       restaurant.name,
		       ` + ownerFirstNameSelectExpression + `,
		       COALESCE(profile.google_place_id, ''),
		       COALESCE(profile.scrape_status, ''),
		       COALESCE(profile.city, ''),
		       COALESCE(profile.cuisines, '[]'::jsonb),
		       profile.rating,
		       profile.reviews_count,
		       lower(trim(restaurant.email)),
		       restaurant.status,
		       restaurant.shown_interest,
		       restaurant.outreach_consent_basis,
		       restaurant.outreach_consent_source,
		       restaurant.outreach_consent_recorded_at,
		       restaurant.outreach_consent_evidence,
		       sequence.status,
		       sequence.signature_name,
		       sequence.signature_title,
		       sequence.signature_details,
		       step.id,
		       step.sequence_id,
		       step.position,
		       step.enabled,
		       step.delay_hours,
		       step.subject_template,
		       step.body_text_template,
		       step.created_at,
		       step.updated_at
		FROM email_campaigns campaign
		JOIN restaurants restaurant ON restaurant.id = campaign.restaurant_id
		LEFT JOIN restaurant_profiles profile ON profile.restaurant_id = restaurant.id
		JOIN outreach_email_sequences sequence ON sequence.id = campaign.sequence_id
		JOIN outreach_email_sequence_steps step
		  ON step.sequence_id = campaign.sequence_id
		 AND step.position = $2
		WHERE campaign.id = $1
		  AND campaign.campaign_type = 'outreach'
		  AND campaign.sequence_id IS NOT NULL
		  AND campaign.status = 'approved'
		  AND campaign.next_step = $2
		  AND campaign.next_send_at <= now()
		  AND (
		    SELECT count(*)
		    FROM restaurants shared_restaurant
		    WHERE lower(trim(shared_restaurant.email)) = lower(trim(restaurant.email))
		  ) <= 3`
	var delivery SequenceDelivery
	if err := repo.pool.QueryRow(ctx, query, campaignID, position).Scan(
		&delivery.CampaignID,
		&delivery.RestaurantID,
		&delivery.RestaurantName,
		&delivery.OwnerFirstName,
		&delivery.GreetingFacts.GooglePlaceID,
		&delivery.GreetingFacts.ScrapeStatus,
		&delivery.GreetingFacts.City,
		&delivery.GreetingFacts.Cuisines,
		&delivery.GreetingFacts.Rating,
		&delivery.GreetingFacts.ReviewCount,
		&delivery.RecipientEmail,
		&delivery.LifecycleStatus,
		&delivery.ShownInterest,
		&delivery.ConsentBasis,
		&delivery.ConsentSource,
		&delivery.ConsentRecordedAt,
		&delivery.ConsentEvidence,
		&delivery.SequenceStatus,
		&delivery.Signature.Name,
		&delivery.Signature.Title,
		&delivery.Signature.AdditionalDetails,
		&delivery.Step.ID,
		&delivery.Step.SequenceID,
		&delivery.Step.Position,
		&delivery.Step.Enabled,
		&delivery.Step.DelayHours,
		&delivery.Step.SubjectTemplate,
		&delivery.Step.BodyTextTemplate,
		&delivery.Step.CreatedAt,
		&delivery.Step.UpdatedAt,
	); errors.Is(err, pgx.ErrNoRows) {
		return SequenceDelivery{}, fmt.Errorf("%w: sequence step is not due", campaigns.ErrNotEligible)
	} else if err != nil {
		return SequenceDelivery{}, fmt.Errorf("load sequence delivery: %w", err)
	}
	delivery.GreetingFacts.RestaurantName = delivery.RestaurantName
	delivery.GreetingFacts.OwnerFirstName = delivery.OwnerFirstName
	return delivery, nil
}

func (repo *Postgres) PrepareSequenceDelivery(ctx context.Context, campaignID uuid.UUID, step int, subject, bodyHTML, bodyText string) error {
	if repo.pool == nil {
		return fmt.Errorf("database pool is not configured")
	}
	result, err := repo.pool.Exec(ctx, `
		UPDATE email_campaigns campaign
		SET subject = $3,
		    body_html = $4,
		    body_text = $5,
		    demo_token = '',
		    updated_at = now()
		FROM restaurants restaurant
		WHERE campaign.id = $1
		  AND restaurant.id = campaign.restaurant_id
		  AND campaign.next_step = $2
		  AND campaign.status = 'approved'
		  AND campaign.next_send_at <= now()
		  AND (
		    SELECT count(*)
		    FROM restaurants shared_restaurant
		    WHERE lower(trim(shared_restaurant.email)) = lower(trim(restaurant.email))
		  ) <= 3`, campaignID, step, subject, bodyHTML, bodyText)
	if err != nil {
		return fmt.Errorf("prepare sequence delivery: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("%w: sequence step changed before delivery", campaigns.ErrNotEligible)
	}
	return nil
}

const nextSequenceDueAtQuery = `
	SELECT min(campaign.next_send_at)
	FROM email_campaigns campaign
	JOIN restaurants restaurant ON restaurant.id = campaign.restaurant_id
	JOIN outreach_email_sequences sequence ON sequence.id = campaign.sequence_id
	JOIN outreach_email_sequence_steps step
	  ON step.sequence_id = campaign.sequence_id
	 AND step.position = campaign.next_step
	WHERE campaign.campaign_type = 'outreach'
	  AND campaign.sequence_id IS NOT NULL
	  AND campaign.status = 'approved'
	  AND campaign.current_step > 0
	  AND campaign.next_step IS NOT NULL
	  AND campaign.next_send_at IS NOT NULL
	  AND sequence.status IN ('approved', 'archived')
	  AND sequence.approved_at IS NOT NULL
	  AND step.enabled = true
	  AND length(trim(restaurant.name)) > 0
	  AND lower(trim(restaurant.email)) ~ '^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$'
	  AND (
	    SELECT count(*)
	    FROM restaurants shared_restaurant
	    WHERE lower(trim(shared_restaurant.email)) = lower(trim(restaurant.email))
	  ) <= 3
	  AND restaurant.status IN ('lead', 'emailed')
	  AND restaurant.shown_interest = false
	  AND restaurant.outreach_consent_basis = 'inferred_business'
	  AND restaurant.outreach_consent_recorded_at IS NOT NULL
	  AND length(trim(restaurant.outreach_consent_source)) > 0
	  AND jsonb_typeof(restaurant.outreach_consent_evidence) = 'object'
	  AND restaurant.outreach_consent_evidence <> '{}'::jsonb
	  AND NOT EXISTS (
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
	  )
	  AND (
	    SELECT count(*)
	    FROM email_delivery_attempts prior_attempt
	    WHERE prior_attempt.campaign_id = campaign.id
	      AND prior_attempt.campaign_step = campaign.next_step
	      AND prior_attempt.status <> 'skipped'
	  ) < 3`

func (repo *Postgres) NextSequenceDueAt(ctx context.Context) (*time.Time, error) {
	if repo.pool == nil {
		return nil, fmt.Errorf("database pool is not configured")
	}
	var next *time.Time
	if err := repo.pool.QueryRow(ctx, nextSequenceDueAtQuery).Scan(&next); err != nil {
		return nil, fmt.Errorf("get next sequence due time: %w", err)
	}
	return next, nil
}

func (repo *Postgres) CountEligibleLeads(ctx context.Context) (int, error) {
	if repo.pool == nil {
		return 0, fmt.Errorf("database pool is not configured")
	}

	query := `SELECT COUNT(*) ` + eligibleLeadsBaseQuery
	var count int
	if err := repo.pool.QueryRow(ctx, query).Scan(&count); err != nil {
		return 0, fmt.Errorf("count eligible leads: %w", err)
	}
	return count, nil
}

const recipientStatusCountsQuery = `
		WITH recipient_state AS MATERIALIZED (
		  SELECT campaign.current_step,
		         campaign.next_step,
		         campaign.next_send_at,
		         campaign.completed_at,
		         campaign.status AS campaign_status,
		         sequence.status AS sequence_status,
		         sequence.approved_at IS NOT NULL AS sequence_approved,
		         COALESCE(step.enabled, false) AS step_enabled,
		         length(trim(restaurant.name)) > 0 AS has_name,
		         lower(trim(restaurant.email)) ~ '^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$' AS has_email,
		         (
		           SELECT count(*)
		           FROM restaurants shared_restaurant
		           WHERE lower(trim(shared_restaurant.email)) = lower(trim(restaurant.email))
		         ) <= 3 AS shared_email_eligible,
		         restaurant.status IN ('lead', 'emailed') AS lifecycle_eligible,
		         restaurant.shown_interest = false AS has_no_interest,
		         restaurant.outreach_consent_basis = 'inferred_business'
		           AND restaurant.outreach_consent_recorded_at IS NOT NULL
		           AND length(trim(restaurant.outreach_consent_source)) > 0
		           AND jsonb_typeof(restaurant.outreach_consent_evidence) = 'object'
		           AND restaurant.outreach_consent_evidence <> '{}'::jsonb AS has_consent_evidence,
		         NOT EXISTS (
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
		         ) AS has_no_blocking_step_outcome,
		         (
		           SELECT count(*)
		           FROM email_delivery_attempts prior_attempt
		           WHERE prior_attempt.campaign_id = campaign.id
		             AND prior_attempt.campaign_step = campaign.next_step
		             AND prior_attempt.status <> 'skipped'
		         ) < 3 AS has_attempt_capacity
		  FROM email_campaigns campaign
		  JOIN restaurants restaurant ON restaurant.id = campaign.restaurant_id
		  JOIN outreach_email_sequences sequence ON sequence.id = campaign.sequence_id
		  LEFT JOIN outreach_email_sequence_steps step
		    ON step.sequence_id = campaign.sequence_id
		   AND step.position = campaign.next_step
		  WHERE campaign.campaign_type = 'outreach'
		    AND campaign.sequence_id IS NOT NULL
		), policy_state AS MATERIALIZED (
		  SELECT *,
		         campaign_status = 'approved'
		           AND sequence_status IN ('approved', 'archived')
		           AND sequence_approved
		           AND step_enabled
		           AND has_name
		           AND has_email
		           AND shared_email_eligible
		           AND lifecycle_eligible
		           AND has_no_interest
		           AND has_consent_evidence
		           AND has_no_blocking_step_outcome
		           AND has_attempt_capacity
		           AND next_step IS NOT NULL
		           AND next_send_at IS NOT NULL AS policy_eligible
		  FROM recipient_state
		)
		SELECT count(*) FILTER (
		         WHERE policy_eligible AND current_step > 0 AND next_send_at <= now()
		       ),
		       count(*) FILTER (
		         WHERE policy_eligible AND current_step = 0 AND next_send_at <= now()
		       ),
		       count(*) FILTER (
		         WHERE completed_at IS NULL AND NOT policy_eligible
		       ),
		       count(*) FILTER (
		         WHERE completed_at IS NOT NULL
		       )
		FROM policy_state`

func (repo *Postgres) CountRecipientStatuses(ctx context.Context) (RecipientStatusCounts, error) {
	if repo.pool == nil {
		return RecipientStatusCounts{}, fmt.Errorf("database pool is not configured")
	}
	var counts RecipientStatusCounts
	if err := repo.pool.QueryRow(ctx, recipientStatusCountsQuery).Scan(
		&counts.DueFollowups, &counts.NewRecipients, &counts.Paused, &counts.Completed,
	); err != nil {
		return RecipientStatusCounts{}, fmt.Errorf("count outreach recipient states: %w", err)
	}
	return counts, nil
}

const sentDeliveryCountsQuery = `
		SELECT count(*),
		       count(*) FILTER (WHERE campaign_step = 1),
		       count(*) FILTER (WHERE campaign_step = 2),
		       count(*) FILTER (WHERE campaign_step = 3),
		       count(*) FILTER (WHERE campaign_step NOT IN (1, 2, 3))
		FROM email_delivery_attempts
		WHERE status = 'sent'`

func (repo *Postgres) CountSentDeliveriesByPhase(ctx context.Context) (SentCounts, error) {
	if repo.pool == nil {
		return SentCounts{}, fmt.Errorf("database pool is not configured")
	}
	var counts SentCounts
	if err := repo.pool.QueryRow(ctx, sentDeliveryCountsQuery).Scan(
		&counts.Total, &counts.Phase1, &counts.Phase2, &counts.Phase3, &counts.Other,
	); err != nil {
		return SentCounts{}, fmt.Errorf("count sent outreach deliveries by phase: %w", err)
	}
	return counts, nil
}

func HasActiveBulkJob(ctx context.Context, pool *pgxpool.Pool, jobType string) (bool, string, error) {
	if pool == nil {
		return false, "", nil
	}

	const query = `
		SELECT id::text
		FROM job_runs
		WHERE job_type = $1 AND status IN ('queued', 'running')
		ORDER BY created_at DESC
		LIMIT 1`

	var jobID string
	err := pool.QueryRow(ctx, query, jobType).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("check active bulk job: %w", err)
	}
	return true, jobID, nil
}

func GetLatestBulkJobSummary(ctx context.Context, pool *pgxpool.Pool, jobType string) (*CompletedJobStatus, error) {
	if pool == nil {
		return nil, nil
	}

	const query = `
		SELECT id::text, status, payload
		FROM job_runs
		WHERE job_type = $1 AND status IN ('completed', 'failed')
		ORDER BY updated_at DESC
		LIMIT 1`

	var jobID string
	var status string
	var payload []byte
	err := pool.QueryRow(ctx, query, jobType).Scan(&jobID, &status, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get latest bulk job: %w", err)
	}

	summary, err := decodeBulkSummary(payload)
	if err != nil {
		return nil, err
	}

	return &CompletedJobStatus{
		JobID:   jobID,
		Status:  status,
		Summary: summary,
	}, nil
}

var _ Repository = (*Postgres)(nil)
