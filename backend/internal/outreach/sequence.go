package outreach

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/auth"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/campaigns"
	repository "github.com/rajchodisetti/restaurant-platform/backend/internal/platform/errors"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/restaurants"
)

const (
	SequenceStatusDraft    = "draft"
	SequenceStatusApproved = "approved"
	SequenceStatusArchived = "archived"

	websiteURL = "https://tuvisolutions.com"
)

var (
	templatePlaceholderPattern = regexp.MustCompile(`\{\{[^{}\r\n]+\}\}`)
	bracketPlaceholderPattern  = regexp.MustCompile(`\[[A-Za-z][A-Za-z0-9_ ]*\]`)
	rawURLPattern              = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
	bareDomainPattern          = regexp.MustCompile(`(?i)(?:www\.)?(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}(?::[0-9]{1,5})?(?:/[^\s<>"']*)?`)
	htmlElementPattern         = regexp.MustCompile(`(?i)<[a-z][^>]*>`)
	validLeadEmailPattern      = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

	ErrSequenceInvalid            = errors.New("outreach sequence is invalid")
	ErrSequenceStale              = errors.New("outreach sequence changed after it was loaded")
	ErrGreetingRestaurantNotFound = errors.New("greeting restaurant was not found")
)

type Sequence struct {
	ID         uuid.UUID         `json:"id"`
	Name       string            `json:"name"`
	Version    int               `json:"version"`
	Status     string            `json:"status"`
	IsActive   bool              `json:"is_active"`
	ApprovedAt *time.Time        `json:"approved_at,omitempty"`
	ApprovedBy *uuid.UUID        `json:"approved_by,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	Signature  SequenceSignature `json:"signature"`
	Steps      []SequenceStep    `json:"steps"`
}

type SequenceSignature struct {
	Name              string `json:"name"`
	Title             string `json:"title"`
	AdditionalDetails string `json:"additional_details"`
}

type SequenceStep struct {
	ID               uuid.UUID `json:"id"`
	SequenceID       uuid.UUID `json:"sequence_id"`
	Position         int       `json:"position"`
	Enabled          bool      `json:"enabled"`
	DelayHours       int       `json:"delay_hours"`
	SubjectTemplate  string    `json:"subject_template"`
	BodyTextTemplate string    `json:"body_text_template"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type SequenceList struct {
	ActiveSequenceID *uuid.UUID `json:"active_sequence_id,omitempty"`
	Sequences        []Sequence `json:"sequences"`
}

type CreateSequenceInput struct {
	Name              string     `json:"name"`
	BasedOnSequenceID *uuid.UUID `json:"based_on_sequence_id,omitempty"`
}

type UpdateSequenceInput struct {
	Name              string            `json:"name"`
	ExpectedUpdatedAt time.Time         `json:"expected_updated_at"`
	Signature         SequenceSignature `json:"signature"`
	Steps             []SequenceStep    `json:"steps"`
}

type PreviewSequenceInput struct {
	RestaurantID   *uuid.UUID `json:"restaurant_id,omitempty"`
	RestaurantName string     `json:"restaurant_name,omitempty"`
	OwnerFirstName string     `json:"owner_first_name,omitempty"`
}

type RenderedSequenceStep struct {
	Position   int    `json:"position"`
	DelayHours int    `json:"delay_hours"`
	Subject    string `json:"subject"`
	BodyText   string `json:"body_text"`
	URLCount   int    `json:"url_count"`
}

type SequencePreview struct {
	RestaurantID   *uuid.UUID             `json:"restaurant_id,omitempty"`
	RestaurantName string                 `json:"restaurant_name"`
	Greeting       string                 `json:"greeting"`
	Greeting01     string                 `json:"greeting01"`
	FactsUsed      []string               `json:"facts_used"`
	Signature      SequenceSignature      `json:"signature"`
	Steps          []RenderedSequenceStep `json:"steps"`
}

type RestaurantGreetingPreview struct {
	RestaurantID   uuid.UUID `json:"restaurant_id"`
	RestaurantName string    `json:"restaurant_name"`
	Greeting       string    `json:"greeting"`
	Greeting01     string    `json:"greeting01"`
	FactsUsed      []string  `json:"facts_used"`
}

type RecipientProgress struct {
	RestaurantID     uuid.UUID  `json:"restaurant_id"`
	RestaurantName   string     `json:"restaurant_name"`
	Email            string     `json:"email"`
	EmailRecordCount int        `json:"email_record_count"`
	LifecycleStatus  string     `json:"lifecycle_status"`
	ConsentBasis     string     `json:"consent_basis"`
	CurrentStep      int        `json:"current_step"`
	NextStep         *int       `json:"next_step,omitempty"`
	NextSendAt       *time.Time `json:"next_send_at,omitempty"`
	LastSentAt       *time.Time `json:"last_sent_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	EmailSendCount   int        `json:"email_send_count"`
	CampaignStatus   string     `json:"campaign_status"`
	Eligible         bool       `json:"eligible"`
	HoldReason       string     `json:"hold_reason,omitempty"`
}

type RecipientProgressList struct {
	Recipients []RecipientProgress `json:"recipients"`
	Total      int                 `json:"total"`
}

type SequenceDelivery struct {
	CampaignID        uuid.UUID
	RestaurantID      uuid.UUID
	RestaurantName    string
	OwnerFirstName    string
	RecipientEmail    string
	LifecycleStatus   string
	ShownInterest     bool
	ConsentBasis      string
	ConsentSource     string
	ConsentRecordedAt *time.Time
	ConsentEvidence   json.RawMessage
	SequenceStatus    string
	Signature         SequenceSignature
	Step              SequenceStep
	GreetingFacts     GreetingFacts
}

type activeSequenceStepsRepository interface {
	ListActiveSequenceSteps(ctx context.Context) ([]SequenceStep, error)
}

type sequenceStepsRepository interface {
	ListSequenceSteps(ctx context.Context, sequenceID uuid.UUID) ([]SequenceStep, error)
}

type sequenceSignatureRepository interface {
	GetSequenceSignature(ctx context.Context, sequenceID uuid.UUID) (SequenceSignature, error)
}

type activeSequenceSignatureRepository interface {
	GetActiveSequenceSignature(ctx context.Context) (SequenceSignature, error)
}

func DefaultSequenceSignature() SequenceSignature {
	return SequenceSignature{
		Name:  "Praveen Maurya",
		Title: "Business Development Manager",
	}
}

type greetingFactsRepository interface {
	GetGreetingFacts(ctx context.Context, restaurantID uuid.UUID) (GreetingFacts, error)
}

const rebaseUntouchedEnrollmentsQuery = `
	WITH first_enabled AS (
	  SELECT position
	  FROM outreach_email_sequence_steps
	  WHERE sequence_id = $1 AND enabled = true
	  ORDER BY position
	  LIMIT 1
	)
	UPDATE email_campaigns campaign
	SET sequence_id = $1,
	    next_step = (SELECT position FROM first_enabled),
	    next_send_at = now(),
	    subject = '',
	    body_html = '',
	    body_text = '',
	    demo_token = '',
	    updated_at = now()
	WHERE campaign.campaign_type = 'outreach'
	  AND campaign.sequence_id IS NOT NULL
	  AND campaign.status = 'approved'
	  AND campaign.current_step = 0
	  AND campaign.last_sent_at IS NULL
	  AND campaign.completed_at IS NULL
	  AND NOT EXISTS (
	    SELECT 1 FROM email_delivery_attempts attempt
	    WHERE attempt.campaign_id = campaign.id
	  )
	  AND NOT EXISTS (
	    SELECT 1 FROM email_events event
	    WHERE event.campaign_id = campaign.id
	      AND event.event_type IN ('sent', 'skipped', 'failed')
	  )`

const listSequencesQuery = `
	SELECT id, name, version, status, is_active,
	       signature_name, signature_title, signature_details,
	       approved_at, approved_by, created_at, updated_at
	FROM outreach_email_sequences
	ORDER BY created_at DESC, version DESC`

const updateSequenceDraftQuery = `
	UPDATE outreach_email_sequences
	SET name = $2,
	    signature_name = $3,
	    signature_title = $4,
	    signature_details = $5,
	    updated_at = now()
	WHERE id = $1
	RETURNING id, name, version, status, is_active,
	          signature_name, signature_title, signature_details,
	          approved_at, approved_by, created_at, updated_at`

func (service *Service) ListSequences(ctx context.Context, principal auth.Principal) (SequenceList, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return SequenceList{}, restaurants.ErrForbidden
	}
	if service.pool == nil {
		return SequenceList{}, fmt.Errorf("database pool is not configured")
	}
	rows, err := service.pool.Query(ctx, listSequencesQuery)
	if err != nil {
		return SequenceList{}, fmt.Errorf("list outreach sequences: %w", err)
	}
	defer rows.Close()
	result := SequenceList{Sequences: []Sequence{}}
	for rows.Next() {
		var sequence Sequence
		if err := rows.Scan(
			&sequence.ID, &sequence.Name, &sequence.Version, &sequence.Status,
			&sequence.IsActive,
			&sequence.Signature.Name, &sequence.Signature.Title, &sequence.Signature.AdditionalDetails,
			&sequence.ApprovedAt, &sequence.ApprovedBy,
			&sequence.CreatedAt, &sequence.UpdatedAt,
		); err != nil {
			return SequenceList{}, fmt.Errorf("scan outreach sequence: %w", err)
		}
		if sequence.IsActive {
			id := sequence.ID
			result.ActiveSequenceID = &id
		}
		result.Sequences = append(result.Sequences, sequence)
	}
	if err := rows.Err(); err != nil {
		return SequenceList{}, fmt.Errorf("iterate outreach sequences: %w", err)
	}
	for index := range result.Sequences {
		steps, err := service.listSequenceSteps(ctx, result.Sequences[index].ID)
		if err != nil {
			return SequenceList{}, err
		}
		result.Sequences[index].Steps = steps
	}
	return result, nil
}

func (service *Service) CreateSequenceDraft(ctx context.Context, principal auth.Principal, input CreateSequenceInput) (Sequence, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return Sequence{}, restaurants.ErrForbidden
	}
	if service.pool == nil {
		return Sequence{}, fmt.Errorf("database pool is not configured")
	}
	name := cleanSingleLine(input.Name)
	if name == "" {
		name = "Tuvi restaurant introduction"
	}
	if len(name) > 120 {
		return Sequence{}, fmt.Errorf("%w: name must be at most 120 characters", ErrSequenceInvalid)
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Sequence{}, fmt.Errorf("begin outreach sequence draft: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	baseID := input.BasedOnSequenceID
	if baseID == nil {
		var active uuid.UUID
		if err := tx.QueryRow(ctx, `
			SELECT id FROM outreach_email_sequences
			WHERE is_active = true AND status = 'approved'
			LIMIT 1`).Scan(&active); err == nil {
			baseID = &active
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return Sequence{}, fmt.Errorf("load active outreach sequence: %w", err)
		}
	}
	var sequence Sequence
	err = tx.QueryRow(ctx, `
		INSERT INTO outreach_email_sequences (name, version, status, is_active)
		VALUES (
		  $1,
		  COALESCE((SELECT max(version) + 1 FROM outreach_email_sequences WHERE name = $1), 1),
		  'draft',
		  false
		)
		RETURNING id, name, version, status, is_active,
		          signature_name, signature_title, signature_details,
		          approved_at, approved_by, created_at, updated_at`, name).Scan(
		&sequence.ID, &sequence.Name, &sequence.Version, &sequence.Status,
		&sequence.IsActive,
		&sequence.Signature.Name, &sequence.Signature.Title, &sequence.Signature.AdditionalDetails,
		&sequence.ApprovedAt, &sequence.ApprovedBy,
		&sequence.CreatedAt, &sequence.UpdatedAt,
	)
	if err != nil {
		return Sequence{}, fmt.Errorf("create outreach sequence draft: %w", err)
	}
	if baseID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE outreach_email_sequences draft
			SET signature_name = source.signature_name,
			    signature_title = source.signature_title,
			    signature_details = source.signature_details
			FROM outreach_email_sequences source
			WHERE draft.id = $1 AND source.id = $2`, sequence.ID, *baseID); err != nil {
			return Sequence{}, fmt.Errorf("copy outreach sequence signature: %w", err)
		}
		result, err := tx.Exec(ctx, `
			INSERT INTO outreach_email_sequence_steps (
			  sequence_id, position, enabled, delay_hours, subject_template, body_text_template
			)
			SELECT $1, position, enabled, delay_hours, subject_template, body_text_template
			FROM outreach_email_sequence_steps
			WHERE sequence_id = $2
			ORDER BY position`, sequence.ID, *baseID)
		if err != nil {
			return Sequence{}, fmt.Errorf("copy outreach sequence steps: %w", err)
		}
		if result.RowsAffected() == 0 {
			return Sequence{}, fmt.Errorf("%w: base sequence was not found or has no steps", ErrSequenceInvalid)
		}
		if err := tx.QueryRow(ctx, `
			SELECT signature_name, signature_title, signature_details
			FROM outreach_email_sequences WHERE id = $1`, sequence.ID).Scan(
			&sequence.Signature.Name, &sequence.Signature.Title, &sequence.Signature.AdditionalDetails,
		); err != nil {
			return Sequence{}, fmt.Errorf("load copied outreach sequence signature: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Sequence{}, fmt.Errorf("commit outreach sequence draft: %w", err)
	}
	sequence.Steps, err = service.listSequenceSteps(ctx, sequence.ID)
	return sequence, err
}

func (service *Service) UpdateSequenceDraft(ctx context.Context, principal auth.Principal, sequenceID uuid.UUID, input UpdateSequenceInput) (Sequence, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return Sequence{}, restaurants.ErrForbidden
	}
	if input.ExpectedUpdatedAt.IsZero() {
		return Sequence{}, fmt.Errorf("%w: expected_updated_at is required", ErrSequenceInvalid)
	}
	name := cleanSingleLine(input.Name)
	if name == "" || len(name) > 120 {
		return Sequence{}, fmt.Errorf("%w: name must be between 1 and 120 characters", ErrSequenceInvalid)
	}
	steps := append([]SequenceStep(nil), input.Steps...)
	if err := validateSequenceSteps(steps); err != nil {
		return Sequence{}, err
	}
	signature, err := normalizeSequenceSignature(input.Signature)
	if err != nil {
		return Sequence{}, err
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Sequence{}, fmt.Errorf("begin outreach sequence update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	var currentUpdatedAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT status, updated_at FROM outreach_email_sequences WHERE id = $1 FOR UPDATE`, sequenceID).Scan(&status, &currentUpdatedAt); errors.Is(err, pgx.ErrNoRows) {
		return Sequence{}, repository.ErrNotFound
	} else if err != nil {
		return Sequence{}, fmt.Errorf("lock outreach sequence: %w", err)
	}
	if status != SequenceStatusDraft {
		return Sequence{}, fmt.Errorf("%w: only draft versions can be edited", ErrSequenceInvalid)
	}
	if !currentUpdatedAt.Equal(input.ExpectedUpdatedAt) {
		return Sequence{}, ErrSequenceStale
	}
	if _, err := tx.Exec(ctx, `DELETE FROM outreach_email_sequence_steps WHERE sequence_id = $1`, sequenceID); err != nil {
		return Sequence{}, fmt.Errorf("replace outreach sequence steps: %w", err)
	}
	for _, step := range steps {
		stepID := step.ID
		if stepID == uuid.Nil {
			stepID = uuid.New()
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO outreach_email_sequence_steps (
			  id, sequence_id, position, enabled, delay_hours, subject_template, body_text_template
			) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			stepID, sequenceID, step.Position, step.Enabled, step.DelayHours,
			strings.TrimSpace(step.SubjectTemplate), strings.TrimSpace(step.BodyTextTemplate),
		); err != nil {
			return Sequence{}, fmt.Errorf("save outreach sequence step %d: %w", step.Position, err)
		}
	}
	var sequence Sequence
	if err := tx.QueryRow(ctx, updateSequenceDraftQuery,
		sequenceID, name, signature.Name, signature.Title, signature.AdditionalDetails).Scan(
		&sequence.ID, &sequence.Name, &sequence.Version, &sequence.Status,
		&sequence.IsActive,
		&sequence.Signature.Name, &sequence.Signature.Title, &sequence.Signature.AdditionalDetails,
		&sequence.ApprovedAt, &sequence.ApprovedBy,
		&sequence.CreatedAt, &sequence.UpdatedAt,
	); err != nil {
		return Sequence{}, fmt.Errorf("update outreach sequence: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Sequence{}, fmt.Errorf("commit outreach sequence update: %w", err)
	}
	sequence.Steps, err = service.listSequenceSteps(ctx, sequence.ID)
	return sequence, err
}

func (service *Service) ApproveSequence(ctx context.Context, principal auth.Principal, sequenceID uuid.UUID, expectedUpdatedAt time.Time) (Sequence, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return Sequence{}, restaurants.ErrForbidden
	}
	if expectedUpdatedAt.IsZero() {
		return Sequence{}, fmt.Errorf("%w: expected_updated_at is required", ErrSequenceInvalid)
	}
	steps, err := service.listSequenceSteps(ctx, sequenceID)
	if err != nil {
		return Sequence{}, err
	}
	if err := validateSequenceSteps(steps); err != nil {
		return Sequence{}, err
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Sequence{}, fmt.Errorf("begin outreach sequence approval: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	var currentUpdatedAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT status, updated_at FROM outreach_email_sequences WHERE id = $1 FOR UPDATE`, sequenceID).Scan(&status, &currentUpdatedAt); errors.Is(err, pgx.ErrNoRows) {
		return Sequence{}, repository.ErrNotFound
	} else if err != nil {
		return Sequence{}, fmt.Errorf("lock outreach sequence for approval: %w", err)
	}
	if status != SequenceStatusDraft {
		return Sequence{}, fmt.Errorf("%w: only a draft version can be approved", ErrSequenceInvalid)
	}
	if !currentUpdatedAt.Equal(expectedUpdatedAt) {
		return Sequence{}, ErrSequenceStale
	}
	if _, err := tx.Exec(ctx, `
		UPDATE outreach_email_sequences
		SET status = 'archived', is_active = false, updated_at = now()
		WHERE is_active = true AND status = 'approved'`); err != nil {
		return Sequence{}, fmt.Errorf("archive prior outreach sequence: %w", err)
	}
	var sequence Sequence
	if err := tx.QueryRow(ctx, `
		UPDATE outreach_email_sequences
		SET status = 'approved', is_active = true, approved_at = now(), approved_by = $2, updated_at = now()
		WHERE id = $1
		RETURNING id, name, version, status, is_active,
		          signature_name, signature_title, signature_details,
		          approved_at, approved_by, created_at, updated_at`, sequenceID, principal.UserID).Scan(
		&sequence.ID, &sequence.Name, &sequence.Version, &sequence.Status,
		&sequence.IsActive,
		&sequence.Signature.Name, &sequence.Signature.Title, &sequence.Signature.AdditionalDetails,
		&sequence.ApprovedAt, &sequence.ApprovedBy,
		&sequence.CreatedAt, &sequence.UpdatedAt,
	); err != nil {
		return Sequence{}, fmt.Errorf("approve outreach sequence: %w", err)
	}
	// Move only untouched enrollments onto the new approved version. Once any
	// provider attempt exists (including an ambiguous one), the campaign remains
	// pinned to its original immutable sequence for auditable follow-ups.
	if _, err := tx.Exec(ctx, rebaseUntouchedEnrollmentsQuery, sequenceID); err != nil {
		return Sequence{}, fmt.Errorf("rebase untouched outreach enrollments: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT ensure_outreach_sequence_enrollment(id) FROM restaurants`); err != nil {
		return Sequence{}, fmt.Errorf("enroll eligible restaurants: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Sequence{}, fmt.Errorf("commit outreach sequence approval: %w", err)
	}
	sequence.Steps = steps
	return sequence, nil
}

func (service *Service) PreviewSequence(ctx context.Context, principal auth.Principal, sequenceID uuid.UUID, input PreviewSequenceInput) (SequencePreview, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return SequencePreview{}, restaurants.ErrForbidden
	}
	steps, err := service.listSequenceSteps(ctx, sequenceID)
	if err != nil {
		return SequencePreview{}, err
	}
	if err := validateSequenceSteps(steps); err != nil {
		return SequencePreview{}, err
	}
	signature, err := service.sequenceSignature(ctx, sequenceID)
	if err != nil {
		return SequencePreview{}, err
	}
	facts, restaurantID, err := service.resolveGreetingFacts(
		ctx, input.RestaurantID, input.RestaurantName, input.OwnerFirstName, "Example Restaurant",
	)
	if err != nil {
		return SequencePreview{}, err
	}
	name := cleanSingleLine(facts.RestaurantName)
	greeting01 := RenderGreeting01(facts)
	preview := SequencePreview{
		RestaurantID: restaurantID, RestaurantName: name,
		Greeting:   outreachGreeting(facts.OwnerFirstName, name),
		Greeting01: greeting01.Greeting01, FactsUsed: greeting01.FactsUsed,
		Signature: signature, Steps: []RenderedSequenceStep{},
	}
	for _, step := range steps {
		if !step.Enabled {
			continue
		}
		rendered, err := renderSequenceStep(step, facts)
		if err != nil {
			return SequencePreview{}, err
		}
		preview.Steps = append(preview.Steps, rendered)
	}
	return preview, nil
}

func (service *Service) PreviewRestaurantGreeting(
	ctx context.Context,
	principal auth.Principal,
	restaurantID uuid.UUID,
) (RestaurantGreetingPreview, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return RestaurantGreetingPreview{}, restaurants.ErrForbidden
	}
	facts, resolvedID, err := service.resolveGreetingFacts(ctx, &restaurantID, "", "", "")
	if err != nil {
		return RestaurantGreetingPreview{}, err
	}
	if resolvedID == nil {
		return RestaurantGreetingPreview{}, ErrGreetingRestaurantNotFound
	}
	name := cleanSingleLine(facts.RestaurantName)
	greeting01 := RenderGreeting01(facts)
	return RestaurantGreetingPreview{
		RestaurantID:   *resolvedID,
		RestaurantName: name,
		Greeting:       outreachGreeting(facts.OwnerFirstName, name),
		Greeting01:     greeting01.Greeting01,
		FactsUsed:      greeting01.FactsUsed,
	}, nil
}

const recipientProgressQuery = `
	SELECT r.id,
	       r.name,
	       r.email,
	       count(*) OVER (PARTITION BY lower(trim(r.email))) AS email_record_count,
	       r.status,
	       r.outreach_consent_basis,
	       COALESCE(c.current_step, 0),
	       c.next_step,
	       c.next_send_at,
	       r.last_email_sent_at,
	       c.completed_at,
	       r.email_send_count,
	       COALESCE(c.status, ''),
	       CASE
	         WHEN trim(r.name) = '' THEN 'missing_name'
	         WHEN lower(trim(r.email)) !~ '^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$' THEN 'invalid_email'
	         WHEN count(*) OVER (PARTITION BY lower(trim(r.email))) > 3 THEN 'shared_email_limit'
	         WHEN r.status NOT IN ('lead', 'emailed') OR r.shown_interest THEN 'lifecycle_paused'
	         WHEN r.outreach_consent_basis <> 'inferred_business'
	           OR r.outreach_consent_recorded_at IS NULL
	           OR trim(r.outreach_consent_source) = ''
	           OR jsonb_typeof(r.outreach_consent_evidence) <> 'object'
	           OR r.outreach_consent_evidence = '{}'::jsonb
	         THEN 'consent_evidence_missing'
	         WHEN c.id IS NULL THEN 'not_enrolled'
	         WHEN c.status = 'send_unknown' THEN 'delivery_unknown'
	         WHEN c.completed_at IS NOT NULL THEN 'complete'
	         WHEN c.status = 'stopped' THEN COALESCE(NULLIF(c.stopped_reason, ''), 'campaign_stopped')
	         WHEN c.status = 'sending' THEN 'delivery_in_progress'
	         WHEN c.status <> 'approved' THEN 'campaign_not_approved'
	         WHEN c.next_step IS NULL OR c.next_send_at IS NULL THEN 'campaign_not_scheduled'
	         WHEN EXISTS (
	           SELECT 1
	           FROM email_delivery_attempts prior_attempt
	           WHERE prior_attempt.campaign_id = c.id
	             AND prior_attempt.campaign_step = c.next_step
	             AND prior_attempt.status = 'failed'
	             AND lower(trim(prior_attempt.error_code)) NOT IN (` + retryableEmailFailureCodesSQL + `)
	         ) THEN 'delivery_failure_not_retryable'
	         WHEN EXISTS (
	           SELECT 1
	           FROM email_delivery_attempts prior_attempt
	           WHERE prior_attempt.campaign_id = c.id
	             AND prior_attempt.campaign_step = c.next_step
	             AND prior_attempt.status = 'unknown'
	         ) THEN 'delivery_unknown'
	         WHEN EXISTS (
	           SELECT 1
	           FROM email_delivery_attempts prior_attempt
	           WHERE prior_attempt.campaign_id = c.id
	             AND prior_attempt.campaign_step = c.next_step
	             AND prior_attempt.status IN ('sent', 'sending')
	         ) THEN 'delivery_outcome_conflict'
	         WHEN (
	           SELECT count(*)
	           FROM email_delivery_attempts prior_attempt
	           WHERE prior_attempt.campaign_id = c.id
	             AND prior_attempt.campaign_step = c.next_step
	             AND prior_attempt.status <> 'skipped'
	         ) >= 3 THEN 'delivery_retry_exhausted'
	         WHEN c.next_send_at > now() THEN 'scheduled'
	         ELSE ''
	       END
	FROM restaurants r
	LEFT JOIN email_campaigns c
	  ON c.restaurant_id = r.id
	 AND c.campaign_type = 'outreach'
	 AND c.sequence_id IS NOT NULL
	ORDER BY c.current_step DESC NULLS LAST, c.next_send_at NULLS LAST, r.created_at
	LIMIT $1 OFFSET $2`

func (service *Service) ListRecipientProgress(ctx context.Context, principal auth.Principal, limit, offset int) (RecipientProgressList, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return RecipientProgressList{}, restaurants.ErrForbidden
	}
	if service.pool == nil {
		return RecipientProgressList{}, fmt.Errorf("database pool is not configured")
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := service.pool.QueryRow(ctx, `SELECT count(*) FROM restaurants`).Scan(&total); err != nil {
		return RecipientProgressList{}, fmt.Errorf("count outreach recipients: %w", err)
	}
	rows, err := service.pool.Query(ctx, recipientProgressQuery, limit, offset)
	if err != nil {
		return RecipientProgressList{}, fmt.Errorf("list outreach recipients: %w", err)
	}
	defer rows.Close()
	result := RecipientProgressList{Recipients: []RecipientProgress{}, Total: total}
	for rows.Next() {
		var record RecipientProgress
		if err := rows.Scan(
			&record.RestaurantID, &record.RestaurantName, &record.Email, &record.EmailRecordCount,
			&record.LifecycleStatus, &record.ConsentBasis, &record.CurrentStep,
			&record.NextStep, &record.NextSendAt, &record.LastSentAt,
			&record.CompletedAt, &record.EmailSendCount, &record.CampaignStatus,
			&record.HoldReason,
		); err != nil {
			return RecipientProgressList{}, fmt.Errorf("scan outreach recipient: %w", err)
		}
		record.Eligible = record.HoldReason == "" || record.HoldReason == "scheduled"
		result.Recipients = append(result.Recipients, record)
	}
	if err := rows.Err(); err != nil {
		return RecipientProgressList{}, fmt.Errorf("iterate outreach recipients: %w", err)
	}
	return result, nil
}

func (service *Service) listSequenceSteps(ctx context.Context, sequenceID uuid.UUID) ([]SequenceStep, error) {
	if repo, ok := service.repo.(sequenceStepsRepository); ok {
		return repo.ListSequenceSteps(ctx, sequenceID)
	}
	if service.pool == nil {
		return nil, fmt.Errorf("database pool is not configured")
	}
	rows, err := service.pool.Query(ctx, `
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
		if err := service.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM outreach_email_sequences WHERE id = $1)`, sequenceID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check outreach sequence: %w", err)
		}
		if !exists {
			return nil, repository.ErrNotFound
		}
	}
	return steps, nil
}

func (service *Service) activeSequenceSteps(ctx context.Context) ([]SequenceStep, error) {
	if repo, ok := service.repo.(activeSequenceStepsRepository); ok {
		return repo.ListActiveSequenceSteps(ctx)
	}
	if service.pool == nil {
		return nil, fmt.Errorf("database pool is not configured")
	}
	rows, err := service.pool.Query(ctx, `
		SELECT step.id, step.sequence_id, step.position, step.enabled, step.delay_hours,
		       step.subject_template, step.body_text_template, step.created_at, step.updated_at
		FROM outreach_email_sequences seq
		JOIN outreach_email_sequence_steps step ON step.sequence_id = seq.id
		WHERE seq.is_active = true
		  AND seq.status = 'approved'
		  AND step.enabled = true
		ORDER BY step.position`)
	if err != nil {
		return nil, fmt.Errorf("list active outreach sequence steps: %w", err)
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
			return nil, fmt.Errorf("scan active outreach sequence step: %w", err)
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active outreach sequence steps: %w", err)
	}
	return steps, nil
}

func (service *Service) sequenceSignature(ctx context.Context, sequenceID uuid.UUID) (SequenceSignature, error) {
	if repo, ok := service.repo.(sequenceSignatureRepository); ok {
		return repo.GetSequenceSignature(ctx, sequenceID)
	}
	if service.pool == nil {
		return DefaultSequenceSignature(), nil
	}
	var signature SequenceSignature
	if err := service.pool.QueryRow(ctx, `
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

func (service *Service) activeSequenceSignature(ctx context.Context) (SequenceSignature, error) {
	repo, ok := service.repo.(activeSequenceSignatureRepository)
	if !ok {
		return SequenceSignature{}, fmt.Errorf("active outreach sequence signature repository is not configured")
	}
	signature, err := repo.GetActiveSequenceSignature(ctx)
	if err != nil {
		return SequenceSignature{}, fmt.Errorf("load active outreach sequence signature: %w", err)
	}
	return signature, nil
}

func validateSequenceSteps(steps []SequenceStep) error {
	if len(steps) == 0 {
		return fmt.Errorf("%w: at least one step is required", ErrSequenceInvalid)
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
	enabled := 0
	firstEnabledChecked := false
	firstEnabledPosition := 0
	for _, step := range steps {
		if step.Enabled {
			firstEnabledPosition = step.Position
			break
		}
	}
	for index, step := range steps {
		if step.Position != index+1 {
			return fmt.Errorf("%w: step positions must be contiguous and 1-based", ErrSequenceInvalid)
		}
		if step.DelayHours < 0 || step.DelayHours > 8760 {
			return fmt.Errorf("%w: step %d delay must be between 0 and 8760 hours", ErrSequenceInvalid, step.Position)
		}
		if step.Enabled {
			enabled++
			if !firstEnabledChecked {
				firstEnabledChecked = true
				if step.DelayHours != 0 {
					return fmt.Errorf("%w: the first enabled step must have a zero-hour delay", ErrSequenceInvalid)
				}
			}
		}
		if err := validateSequenceTemplate(step, step.Position == firstEnabledPosition); err != nil {
			return err
		}
	}
	if enabled == 0 {
		return fmt.Errorf("%w: at least one step must be enabled", ErrSequenceInvalid)
	}
	return nil
}

func validateSequenceTemplate(step SequenceStep, firstEnabled bool) error {
	subject := strings.TrimSpace(step.SubjectTemplate)
	body := strings.TrimSpace(step.BodyTextTemplate)
	if subject == "" || len(subject) > 200 || strings.ContainsAny(subject, "\r\n") {
		return fmt.Errorf("%w: step %d subject must be one line and at most 200 characters", ErrSequenceInvalid, step.Position)
	}
	if body == "" || len(body) > 10000 || htmlElementPattern.MatchString(subject) || htmlElementPattern.MatchString(body) {
		return fmt.Errorf("%w: step %d body must be plain text", ErrSequenceInvalid, step.Position)
	}
	allowed := map[string]bool{
		"{{greeting}}": true, "{{restaurant_name}}": true,
		"{{website_url}}": true, "{{greeting01}}": true,
	}
	for _, value := range append(templatePlaceholderPattern.FindAllString(subject, -1), templatePlaceholderPattern.FindAllString(body, -1)...) {
		if !allowed[value] {
			return fmt.Errorf("%w: step %d contains unsupported placeholder %s", ErrSequenceInvalid, step.Position, value)
		}
	}
	allowedBracket := map[string]bool{
		"[GREETING]": true, "[FIRST_NAME]": true,
		"[RESTAURANT_NAME]": true, "[CUISINE]": true,
		"[CITY]": true, "[RATING]": true,
		"[TOTAL_REVIEWS]": true, "[WEBSITE_URL]": true,
	}
	for _, value := range append(bracketPlaceholderPattern.FindAllString(subject, -1), bracketPlaceholderPattern.FindAllString(body, -1)...) {
		if !allowedBracket[value] {
			return fmt.Errorf("%w: step %d contains unsupported placeholder %s", ErrSequenceInvalid, step.Position, value)
		}
	}
	greeting01SubjectCount := strings.Count(subject, "{{greeting01}}") + strings.Count(subject, "[GREETING]")
	greeting01BodyCount := strings.Count(body, "{{greeting01}}") + strings.Count(body, "[GREETING]")
	if greeting01SubjectCount > 0 {
		return fmt.Errorf("%w: step %d cannot use [GREETING] or {{greeting01}} in the subject", ErrSequenceInvalid, step.Position)
	}
	if greeting01BodyCount > 0 && !firstEnabled {
		return fmt.Errorf("%w: step %d can use the complete greeting only in the first enabled email body", ErrSequenceInvalid, step.Position)
	}
	if greeting01BodyCount > 1 {
		return fmt.Errorf("%w: step %d can use the complete greeting exactly once", ErrSequenceInvalid, step.Position)
	}
	if greeting01BodyCount == 1 && strings.Contains(body, "{{greeting}}") {
		return fmt.Errorf("%w: step %d must replace {{greeting}} when using the complete greeting", ErrSequenceInvalid, step.Position)
	}
	return nil
}

func containsUnmanagedLinkCandidate(value string) bool {
	return rawURLPattern.MatchString(value) || bareDomainPattern.MatchString(value)
}

func renderSequenceStep(step SequenceStep, facts GreetingFacts) (RenderedSequenceStep, error) {
	name := cleanSingleLine(facts.RestaurantName)
	if name == "" {
		return RenderedSequenceStep{}, fmt.Errorf("%w: restaurant name is required", campaigns.ErrNotEligible)
	}
	facts.RestaurantName = name
	greeting := outreachGreeting(facts.OwnerFirstName, name)
	greeting01 := RenderGreeting01(facts).Greeting01
	values := templatePlaceholderValues(facts)
	replacer := strings.NewReplacer(
		"{{greeting}}", greeting,
		"{{greeting01}}", greeting01,
		"{{restaurant_name}}", name,
		"{{website_url}}", websiteURL,
		"[GREETING]", greeting01,
		"[FIRST_NAME]", values.FirstName,
		"[RESTAURANT_NAME]", name,
		"[CUISINE]", values.Cuisine,
		"[CITY]", values.City,
		"[RATING]", values.Rating,
		"[TOTAL_REVIEWS]", values.TotalReviews,
		"[WEBSITE_URL]", websiteURL,
	)
	subject := replacer.Replace(strings.TrimSpace(step.SubjectTemplate))
	body := replacer.Replace(strings.TrimSpace(step.BodyTextTemplate))
	if templatePlaceholderPattern.MatchString(subject+"\n"+body) || bracketPlaceholderPattern.MatchString(subject+"\n"+body) {
		return RenderedSequenceStep{}, fmt.Errorf("%w: rendered step contains an unresolved placeholder", ErrSequenceInvalid)
	}
	urls := rawURLPattern.FindAllString(body, -1)
	return RenderedSequenceStep{
		Position: step.Position, DelayHours: step.DelayHours,
		Subject: subject, BodyText: body, URLCount: len(urls),
	}, nil
}

type templateValues struct {
	FirstName    string
	Cuisine      string
	City         string
	Rating       string
	TotalReviews string
}

func templatePlaceholderValues(facts GreetingFacts) templateValues {
	restaurantName := safeGreetingValue(facts.RestaurantName, maxGreetingRestaurantNameLength)
	if restaurantName == "" {
		restaurantName = "restaurant"
	}
	firstName := safeOwnerFirstName(facts.OwnerFirstName)
	if firstName == "" {
		firstName = restaurantName + " team"
	}
	values := templateValues{
		FirstName: firstName, Cuisine: "N/A", City: "N/A",
		Rating: "N/A", TotalReviews: "N/A",
	}
	verifiedListing := strings.TrimSpace(facts.GooglePlaceID) != "" && strings.TrimSpace(facts.ScrapeStatus) == "success"
	if !verifiedListing {
		return values
	}
	if cuisine := firstSafeRestaurantCuisine(facts.Cuisines); cuisine != "" {
		values.Cuisine = cuisine
	}
	if city := safeGreetingValue(facts.City, maxGreetingCityLength); city != "" {
		values.City = city
	}
	if facts.Rating != nil && !math.IsNaN(*facts.Rating) && !math.IsInf(*facts.Rating, 0) && *facts.Rating >= 0 && *facts.Rating <= 5 {
		values.Rating = strconv.FormatFloat(*facts.Rating, 'f', 1, 64)
	}
	if facts.ReviewCount != nil && *facts.ReviewCount >= 0 {
		values.TotalReviews = strconv.Itoa(*facts.ReviewCount)
	}
	return values
}

func normalizeSequenceSignature(signature SequenceSignature) (SequenceSignature, error) {
	if strings.TrimSpace(signature.Name) == "" && strings.TrimSpace(signature.Title) == "" && strings.TrimSpace(signature.AdditionalDetails) == "" {
		return DefaultSequenceSignature(), nil
	}
	if strings.ContainsAny(signature.Name, "\r\n") || strings.ContainsAny(signature.Title, "\r\n") {
		return SequenceSignature{}, fmt.Errorf("%w: signature name and title must each be one line", ErrSequenceInvalid)
	}
	signature.Name = cleanSingleLine(signature.Name)
	signature.Title = cleanSingleLine(signature.Title)
	signature.AdditionalDetails = strings.TrimSpace(strings.ReplaceAll(signature.AdditionalDetails, "\r\n", "\n"))
	signature.AdditionalDetails = strings.ReplaceAll(signature.AdditionalDetails, "\r", "\n")
	if signature.Name == "" || len(signature.Name) > 120 {
		return SequenceSignature{}, fmt.Errorf("%w: signature name must be between 1 and 120 characters", ErrSequenceInvalid)
	}
	if len(signature.Title) > 160 {
		return SequenceSignature{}, fmt.Errorf("%w: signature title must be 160 characters or fewer", ErrSequenceInvalid)
	}
	if len(signature.AdditionalDetails) > 1000 || htmlElementPattern.MatchString(signature.AdditionalDetails) {
		return SequenceSignature{}, fmt.Errorf("%w: signature details must be plain text and 1000 characters or fewer", ErrSequenceInvalid)
	}
	return signature, nil
}

func (service *Service) resolveGreetingFacts(
	ctx context.Context,
	restaurantID *uuid.UUID,
	restaurantName string,
	ownerFirstName string,
	defaultRestaurantName string,
) (GreetingFacts, *uuid.UUID, error) {
	if restaurantID != nil {
		repo, ok := service.repo.(greetingFactsRepository)
		if !ok {
			return GreetingFacts{}, nil, fmt.Errorf("greeting facts repository is not configured")
		}
		facts, err := repo.GetGreetingFacts(ctx, *restaurantID)
		if errors.Is(err, repository.ErrNotFound) {
			return GreetingFacts{}, nil, ErrGreetingRestaurantNotFound
		}
		if err != nil {
			return GreetingFacts{}, nil, err
		}
		id := *restaurantID
		return facts, &id, nil
	}
	name := cleanSingleLine(restaurantName)
	if name == "" {
		name = defaultRestaurantName
	}
	return GreetingFacts{
		RestaurantName: name,
		OwnerFirstName: cleanFirstName(ownerFirstName),
		Cuisines:       json.RawMessage("[]"),
	}, nil, nil
}

func checkSequenceDeliveryEligibility(delivery SequenceDelivery) error {
	if strings.TrimSpace(delivery.RestaurantName) == "" {
		return fmt.Errorf("%w: restaurant has no name", campaigns.ErrNotEligible)
	}
	if !validLeadEmailPattern.MatchString(strings.ToLower(strings.TrimSpace(delivery.RecipientEmail))) {
		return fmt.Errorf("%w: restaurant has no valid contact email", campaigns.ErrNotEligible)
	}
	if delivery.ShownInterest || delivery.LifecycleStatus == restaurants.StatusInterested {
		return fmt.Errorf("%w: restaurant has expressed interest and automated outreach is paused", campaigns.ErrNotEligible)
	}
	switch delivery.LifecycleStatus {
	case restaurants.StatusLead, restaurants.StatusEmailed:
	default:
		return fmt.Errorf("%w: restaurant lifecycle is not eligible for outreach", campaigns.ErrNotEligible)
	}
	if delivery.ConsentBasis != "inferred_business" ||
		delivery.ConsentRecordedAt == nil ||
		strings.TrimSpace(delivery.ConsentSource) == "" ||
		!validConsentEvidence(delivery.ConsentEvidence) {
		return fmt.Errorf("%w: inferred-business consent evidence is missing", campaigns.ErrNotEligible)
	}
	if delivery.SequenceStatus != SequenceStatusApproved && delivery.SequenceStatus != SequenceStatusArchived {
		return fmt.Errorf("%w: sequence version was not approved", campaigns.ErrNotEligible)
	}
	if !delivery.Step.Enabled {
		return fmt.Errorf("%w: sequence step is disabled", campaigns.ErrNotEligible)
	}
	return nil
}

func validConsentEvidence(raw json.RawMessage) bool {
	var evidence map[string]any
	return len(raw) > 0 && json.Unmarshal(raw, &evidence) == nil && len(evidence) > 0
}

func outreachGreeting(ownerFirstName, restaurantName string) string {
	if first := cleanFirstName(ownerFirstName); first != "" {
		return "Hi " + first + ","
	}
	name := cleanSingleLine(restaurantName)
	if name == "" {
		name = "restaurant"
	}
	return "Hi " + name + " team,"
}

func cleanFirstName(value string) string {
	value = cleanSingleLine(value)
	if value == "" {
		return ""
	}
	value = strings.Fields(value)[0]
	var builder strings.Builder
	for _, char := range value {
		if unicode.IsLetter(char) || char == '-' || char == '\'' {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func cleanSingleLine(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}
