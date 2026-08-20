package outreach

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/auth"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/campaigns"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/platform/config"
	repository "github.com/rajchodisetti/restaurant-platform/backend/internal/platform/errors"
	emailprovider "github.com/rajchodisetti/restaurant-platform/backend/internal/providers/email"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/restaurants"
)

type Service struct {
	repo            Repository
	pool            *pgxpool.Pool
	campaigns       campaigns.Repository
	campaignService *campaigns.Service
	restaurants     *restaurants.Service
	tokenResolver   DemoTokenResolver
	emailPool       emailprovider.AccountPoolProvider
	emailProvider   emailprovider.Provider
	emailCfg        config.EmailConfig
	outreachCfg     config.OutreachConfig
	appURLs         config.AppURLsConfig
	enqueuer        BulkJobEnqueuer
	log             *slog.Logger
}

func NewService(
	repo Repository,
	pool *pgxpool.Pool,
	campaignsRepo campaigns.Repository,
	campaignService *campaigns.Service,
	restaurantsService *restaurants.Service,
	tokenResolver DemoTokenResolver,
	emailPool emailprovider.AccountPoolProvider,
	emailProvider emailprovider.Provider,
	emailCfg config.EmailConfig,
	outreachCfg config.OutreachConfig,
	appURLs config.AppURLsConfig,
	enqueuer BulkJobEnqueuer,
	log *slog.Logger,
) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		repo:            repo,
		pool:            pool,
		campaigns:       campaignsRepo,
		campaignService: campaignService,
		restaurants:     restaurantsService,
		tokenResolver:   tokenResolver,
		emailPool:       emailPool,
		emailProvider:   emailProvider,
		emailCfg:        emailCfg,
		outreachCfg:     outreachCfg,
		appURLs:         appURLs,
		enqueuer:        enqueuer,
		log:             log,
	}
}

type TemplateTestSendInput struct {
	RecipientEmail string     `json:"recipient_email"`
	SequenceID     *uuid.UUID `json:"sequence_id,omitempty"`
	RestaurantID   *uuid.UUID `json:"restaurant_id,omitempty"`
	RestaurantName string     `json:"restaurant_name,omitempty"`
	OwnerFirstName string     `json:"owner_first_name,omitempty"`
}

type TemplateTestSendResult struct {
	RecipientEmail string                    `json:"recipient_email"`
	SequenceID     uuid.UUID                 `json:"sequence_id"`
	RestaurantID   *uuid.UUID                `json:"restaurant_id,omitempty"`
	RestaurantName string                    `json:"restaurant_name"`
	Greeting01     string                    `json:"greeting01"`
	FactsUsed      []string                  `json:"facts_used"`
	Items          []TemplateTestEmailResult `json:"items"`
}

type TemplateTestEmailResult struct {
	Template          string `json:"template"`
	Step              int    `json:"step,omitempty"`
	Subject           string `json:"subject"`
	ProviderMessageID string `json:"provider_message_id,omitempty"`
}

func (service *Service) SendTemplateTest(ctx context.Context, principal auth.Principal, input TemplateTestSendInput) (TemplateTestSendResult, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return TemplateTestSendResult{}, restaurants.ErrForbidden
	}
	if service.emailCfg.DisableSending {
		return TemplateTestSendResult{}, ErrSendingDisabled
	}
	recipient, err := cleanTestRecipient(input.RecipientEmail)
	if err != nil {
		return TemplateTestSendResult{}, err
	}
	var steps []SequenceStep
	if input.SequenceID != nil {
		steps, err = service.listSequenceSteps(ctx, *input.SequenceID)
		if err == nil {
			err = validateSequenceSteps(steps)
		}
		if err == nil {
			enabled := make([]SequenceStep, 0, len(steps))
			for _, step := range steps {
				if step.Enabled {
					enabled = append(enabled, step)
				}
			}
			steps = enabled
		}
	} else {
		steps, err = service.activeSequenceSteps(ctx)
	}
	if err != nil {
		return TemplateTestSendResult{}, err
	}
	if len(steps) == 0 {
		return TemplateTestSendResult{}, fmt.Errorf("%w: selected outreach sequence has no enabled steps", ErrSequenceInvalid)
	}
	sequenceID := steps[0].SequenceID
	signature, err := service.sequenceSignature(ctx, sequenceID)
	if err != nil {
		return TemplateTestSendResult{}, err
	}
	var provider emailprovider.Provider
	if service.emailPool != nil {
		configured, configuredErr := service.emailPool.Configured(ctx)
		if configuredErr != nil {
			return TemplateTestSendResult{}, configuredErr
		}
		if !configured {
			return TemplateTestSendResult{}, ErrNotConfigured
		}
		provider, err = service.emailPool.AcquireDirect(ctx)
		if err != nil {
			return TemplateTestSendResult{}, err
		}
	} else {
		builtProvider, providerErr := emailprovider.NewAccountPoolFromConfig(service.emailCfg, service.outreachCfg)
		if providerErr != nil {
			return TemplateTestSendResult{}, ErrNotConfigured
		}
		provider, providerErr = builtProvider.AcquireDirect(ctx)
		if providerErr != nil {
			return TemplateTestSendResult{}, ErrNotConfigured
		}
	}
	facts, restaurantID, err := service.resolveGreetingFacts(
		ctx, input.RestaurantID, input.RestaurantName, input.OwnerFirstName, "Tuvi Test Restaurant",
	)
	if err != nil {
		return TemplateTestSendResult{}, err
	}
	return service.sendTemplateTestEmails(ctx, provider, recipient, restaurantID, facts, signature, steps)
}

func (service *Service) sendTemplateTestEmails(
	ctx context.Context,
	provider emailprovider.Provider,
	recipientEmail string,
	restaurantID *uuid.UUID,
	facts GreetingFacts,
	signature SequenceSignature,
	steps []SequenceStep,
) (TemplateTestSendResult, error) {
	greeting01 := RenderGreeting01(facts)
	result := TemplateTestSendResult{
		RecipientEmail: recipientEmail,
		SequenceID:     steps[0].SequenceID,
		RestaurantID:   restaurantID,
		RestaurantName: cleanSingleLine(facts.RestaurantName),
		Greeting01:     greeting01.Greeting01,
		FactsUsed:      greeting01.FactsUsed,
		Items:          []TemplateTestEmailResult{},
	}

	for _, step := range steps {
		rendered, err := renderSequenceStep(step, facts)
		if err != nil {
			return result, fmt.Errorf("render sequence step %d: %w", step.Position, err)
		}
		request := ensureSequenceSignature(emailprovider.SendRequest{
			To:       recipientEmail,
			Subject:  rendered.Subject,
			TextBody: rendered.BodyText,
			Metadata: map[string]string{
				"purpose":       "outreach_template_test",
				"template":      "sequence",
				"sequence_step": fmt.Sprintf("%d", step.Position),
			},
		}, signature)
		stepResult, err := provider.Send(ctx, request)
		if err != nil {
			return result, fmt.Errorf("send sequence step %d: %w", step.Position, err)
		}
		result.Items = append(result.Items, TemplateTestEmailResult{
			Template:          "sequence",
			Step:              step.Position,
			Subject:           rendered.Subject,
			ProviderMessageID: stepResult.ProviderMessageID,
		})
	}
	return result, nil
}

func cleanTestRecipient(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", ErrInvalidRecipientEmail
	}
	address, err := mail.ParseAddress(trimmed)
	if err != nil || address.Address != trimmed {
		return "", ErrInvalidRecipientEmail
	}
	return strings.ToLower(address.Address), nil
}

func (service *Service) SetEmailJob(ctx context.Context, principal auth.Principal, enabled bool) (EmailJobActionResult, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return EmailJobActionResult{}, fmt.Errorf("forbidden")
	}
	if !enabled {
		control, err := DisableEmailJobControl(ctx, service.pool)
		return EmailJobActionResult{
			EmailJob: control,
			Status:   "disabled",
			MaxSends: service.outreachCfg.BulkMax,
		}, err
	}
	if service.emailPool == nil {
		return EmailJobActionResult{}, ErrNotConfigured
	}
	configured, err := service.emailPool.Configured(ctx)
	if err != nil {
		return EmailJobActionResult{}, err
	}
	if !configured {
		return EmailJobActionResult{}, ErrNotConfigured
	}
	if err := validateBulkMax(service.outreachCfg.BulkMax); err != nil {
		return EmailJobActionResult{}, err
	}
	pending, err := service.repo.CountEligibleLeads(ctx)
	if err != nil {
		return EmailJobActionResult{}, err
	}
	control, activeJobID, err := EnableEmailJobControl(ctx, service.pool, principal.UserID)
	if err != nil {
		return EmailJobActionResult{}, err
	}
	if activeJobID != "" {
		return EmailJobActionResult{
			EmailJob:             control,
			JobID:                activeJobID,
			Status:               "resuming",
			MaxSends:             service.outreachCfg.BulkMax,
			PendingEligibleCount: pending,
		}, nil
	}
	jobID, err := service.enqueuer.EnqueueBulkSend(ctx, principal.UserID)
	if err != nil {
		// A concurrent admin activation may have won the queue's unique active-job
		// constraint after our read check. Keep the shared control enabled for that
		// winning job; only revert when no active job was created.
		if !errors.Is(err, ErrBulkJobActive) {
			_, _ = SetEmailJobControl(ctx, service.pool, false, nil)
		}
		return EmailJobActionResult{}, err
	}
	return EmailJobActionResult{
		EmailJob:             control,
		JobID:                jobID,
		Status:               "queued",
		MaxSends:             service.outreachCfg.BulkMax,
		PendingEligibleCount: pending,
	}, nil
}

// SettleDisabledBulkJob atomically hands a running disabled job either back to
// the queue when a concurrent re-enable won, or to completed when the control
// remains disabled. Both this method and EnableEmailJobControl lock the control
// row before the job row, closing the enabled-without-an-active-job race.
func (service *Service) SettleDisabledBulkJob(
	ctx context.Context,
	jobID string,
	lockedBy string,
	triggeredBy uuid.UUID,
	summary BulkSendSummary,
) error {
	if service.pool == nil {
		return fmt.Errorf("database pool is not configured")
	}
	payload, err := encodeBulkSummary(summary, triggeredBy.String())
	if err != nil {
		return err
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin settling disabled outreach job: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var enabled bool
	if err := tx.QueryRow(ctx, `
		SELECT enabled FROM outreach_runtime_control
		WHERE control_key = 'email_job'
		FOR UPDATE`).Scan(&enabled); err != nil {
		return fmt.Errorf("lock outreach email control while settling job: %w", err)
	}
	status := "completed"
	availableAt := time.Now().UTC()
	if enabled {
		status = "queued"
	}
	result, err := tx.Exec(ctx, `
		UPDATE job_runs
		SET status = $4,
		    payload = $2,
		    available_at = $5,
		    attempts = CASE WHEN $4 = 'queued' THEN 0 ELSE attempts END,
		    locked_at = NULL,
		    locked_by = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE id = $1::uuid
		  AND job_type = $6
		  AND status = 'running'
		  AND locked_by = $3`,
		jobID, payload, lockedBy, status, availableAt, BulkSendJobType,
	)
	if err != nil {
		return fmt.Errorf("settle disabled outreach job: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("bulk outreach job lease was lost while settling disabled control")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit disabled outreach job settlement: %w", err)
	}
	return nil
}

func (service *Service) GetStatus(ctx context.Context, principal auth.Principal) (StatusResult, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return StatusResult{}, fmt.Errorf("forbidden")
	}

	pending, err := service.repo.CountEligibleLeads(ctx)
	if err != nil {
		return StatusResult{}, err
	}

	result := StatusResult{
		PendingEligibleCount: pending,
		MaxSends:             service.outreachCfg.BulkMax,
	}
	if counter, ok := service.repo.(interface {
		CountRecipientStatuses(context.Context) (RecipientStatusCounts, error)
	}); ok {
		counts, countErr := counter.CountRecipientStatuses(ctx)
		if countErr != nil {
			return StatusResult{}, countErr
		}
		result.DueFollowupCount = counts.DueFollowups
		result.NewRecipientCount = counts.NewRecipients
		result.PausedRecipientCount = counts.Paused
		result.CompletedRecipientCount = counts.Completed
	} else {
		result.NewRecipientCount = pending
	}
	if counter, ok := service.repo.(interface {
		CountSentDeliveriesByPhase(context.Context) (SentCounts, error)
	}); ok {
		counts, countErr := counter.CountSentDeliveriesByPhase(ctx)
		if countErr != nil {
			return StatusResult{}, countErr
		}
		result.SentCounts = counts
	}
	control, err := GetEmailJobControl(ctx, service.pool)
	if err != nil {
		return StatusResult{}, err
	}
	result.EmailJob = control
	schedule, err := GetEmailSendSchedule(ctx, service.pool)
	if err != nil {
		return StatusResult{}, err
	}
	result.SendSchedule = schedule
	configured := false
	if service.emailPool != nil {
		configured, err = service.emailPool.Configured(ctx)
		if err != nil {
			return StatusResult{}, err
		}
	}
	if configured && service.emailPool.Durable() {
		nextAvailableAt, err := service.emailPool.NextAvailableAt(ctx)
		if err != nil {
			return StatusResult{}, err
		}
		result.NextAvailableAt = nextAvailableAt
	}

	active, activeJobID, err := HasActiveBulkJob(ctx, service.pool, BulkSendJobType)
	if err != nil {
		return StatusResult{}, err
	}
	if active {
		result.ActiveJob = &ActiveJobStatus{
			JobID:  activeJobID,
			Status: "running_or_queued",
		}
	}

	lastJob, err := GetLatestBulkJobSummary(ctx, service.pool, BulkSendJobType)
	if err != nil {
		return StatusResult{}, err
	}
	result.LastCompletedJob = lastJob
	return result, nil
}

func (service *Service) RunBulkSend(ctx context.Context, triggeredBy uuid.UUID, jobIDs ...string) (BulkSendSummary, error) {
	summary := BulkSendSummary{MaxSends: service.outreachCfg.BulkMax}
	var bulkJobID *uuid.UUID
	if len(jobIDs) > 0 && jobIDs[0] != "" {
		if parsed, err := uuid.Parse(jobIDs[0]); err == nil {
			bulkJobID = &parsed
			persisted, loadErr := service.loadBulkJobSummary(ctx, parsed)
			if loadErr != nil {
				return summary, loadErr
			}
			summary = persisted
			summary.MaxSends = service.outreachCfg.BulkMax
			summary.StoppedReason = ""
			summary.NextAvailableAt = nil
		}
	}
	startedAttempted := summary.Attempted

	jobControl, err := GetEmailJobControl(ctx, service.pool)
	if err != nil {
		return summary, err
	}
	if service.pool != nil && !jobControl.Enabled {
		summary.StoppedReason = "disabled_by_admin"
		return summary, nil
	}
	if service.emailPool == nil {
		return summary, ErrNotConfigured
	}
	configured, err := service.emailPool.Configured(ctx)
	if err != nil {
		return summary, err
	}
	if !configured {
		return summary, ErrNotConfigured
	}
	if err := validateBulkMax(service.outreachCfg.BulkMax); err != nil {
		return summary, err
	}
	leadLimit := service.outreachCfg.BulkMax
	if service.emailPool.Durable() {
		// A durable execution crosses the provider boundary at most once. The
		// same job is then released until PostgreSQL says the next slot is due.
		leadLimit = 1
	}
	leads, err := service.repo.ListEligibleLeads(ctx, leadLimit)
	if err != nil {
		return summary, err
	}

	if len(leads) == 0 {
		summary.StoppedReason = "waiting_for_leads"
		pollAt := time.Now().UTC().Add(5 * time.Minute)
		if sequenceRepo, ok := service.repo.(sequenceDeliveryRepository); ok {
			dueAt, dueErr := sequenceRepo.NextSequenceDueAt(ctx)
			if dueErr != nil {
				return summary, dueErr
			}
			if dueAt != nil && dueAt.Before(pollAt) {
				pollAt = dueAt.UTC()
				summary.StoppedReason = "waiting_for_followup"
			}
		}
		summary.NextAvailableAt = &pollAt
		return summary, nil
	}

	for _, lead := range leads {
		if service.pool != nil {
			control, controlErr := GetEmailJobControl(ctx, service.pool)
			if controlErr != nil {
				return summary, controlErr
			}
			if !control.Enabled {
				summary.StoppedReason = "disabled_by_admin"
				break
			}
		}
		if !service.emailPool.Durable() && service.emailPool.Exhausted() {
			summary.StoppedReason = "account_limit_reached"
			break
		}

		sent, err := service.sendLead(ctx, lead, bulkJobID)
		if err != nil {
			if errors.Is(err, emailprovider.ErrAccountsExhausted) {
				summary.StoppedReason = "paced"
				nextAvailableAt, availabilityErr := service.emailPool.NextAvailableAt(ctx)
				if availabilityErr != nil {
					return summary, availabilityErr
				}
				if nextAvailableAt == nil {
					// An account can cross its availability boundary between the
					// failed claim and this lookup. Requeue briefly instead of
					// failing a one-attempt bulk job at that boundary.
					retryAt := time.Now().UTC().Add(time.Second)
					nextAvailableAt = &retryAt
				}
				summary.NextAvailableAt = nextAvailableAt
				break
			}
			if errors.Is(err, emailprovider.ErrAccountUnavailable) {
				summary.Attempted++
				summary.Failed++
				summary.StoppedReason = "account_unavailable"
				nextAvailableAt, availabilityErr := service.emailPool.NextAvailableAt(ctx)
				if availabilityErr != nil {
					_, _ = SetEmailJobControl(ctx, service.pool, false, nil)
					return summary, availabilityErr
				}
				if nextAvailableAt == nil {
					retryAt := time.Now().UTC().Add(time.Second)
					nextAvailableAt = &retryAt
				}
				summary.NextAvailableAt = nextAvailableAt
				break
			}
			if service.emailPool.Durable() && errors.Is(err, emailprovider.ErrRetryableRejection) {
				summary.Attempted++
				summary.Failed++
				// This describes why the current job execution yielded. The
				// repository audit event records whether this particular campaign
				// was scheduled again or exhausted its per-step attempt cap.
				summary.StoppedReason = "retryable_provider_rejection"
				nextAvailableAt, availabilityErr := service.emailPool.NextAvailableAt(ctx)
				if availabilityErr != nil {
					_, _ = SetEmailJobControl(ctx, service.pool, false, nil)
					return summary, availabilityErr
				}
				if nextAvailableAt == nil {
					retryAt := time.Now().UTC().Add(time.Second)
					nextAvailableAt = &retryAt
				}
				summary.NextAvailableAt = nextAvailableAt
				break
			}
			summary.Attempted++
			if errors.Is(err, campaigns.ErrNotEligible) {
				summary.Skipped++
				service.log.InfoContext(ctx, "bulk_outreach_lead_became_ineligible",
					"restaurant_id", lead.RestaurantID,
					"error", err,
				)
				continue
			}
			service.log.WarnContext(ctx, "bulk_outreach_lead_failed",
				"restaurant_id", lead.RestaurantID,
				"error", err,
			)
			summary.Failed++
			summary.StoppedReason = "delivery_error"
			_, _ = SetEmailJobControl(ctx, service.pool, false, nil)
			return summary, err
		} else if sent {
			summary.Attempted++
			summary.Sent++
		} else {
			summary.Attempted++
			summary.Skipped++
		}
	}

	batchAttempted := summary.Attempted - startedAttempted
	if summary.StoppedReason == "" {
		if batchAttempted >= len(leads) {
			summary.StoppedReason = "batch_complete"
		} else if !service.emailPool.Durable() && service.emailPool.Exhausted() {
			summary.StoppedReason = "account_limit_reached"
		}
	}

	// The same human-triggered durable job continues one provider reservation at
	// a time while approved leads remain. PostgreSQL's account and global gates
	// supply the restart-safe next timestamp; no worker sleeps between sends.
	if summary.NextAvailableAt == nil && batchAttempted > 0 {
		remaining, countErr := service.repo.CountEligibleLeads(ctx)
		if countErr != nil {
			return summary, countErr
		}
		if remaining > 0 {
			nextAvailableAt, availabilityErr := service.emailPool.NextAvailableAt(ctx)
			if availabilityErr != nil {
				return summary, availabilityErr
			}
			if nextAvailableAt == nil {
				// The persisted gates can already be due after an eligibility
				// rejection or a slow provider call. Yield briefly; the next claim
				// still rechecks both database gates atomically.
				resumeAt := time.Now().UTC().Add(time.Second)
				nextAvailableAt = &resumeAt
				summary.StoppedReason = "more_eligible_leads"
			} else {
				summary.StoppedReason = "paced"
			}
			summary.NextAvailableAt = nextAvailableAt
		} else {
			pollAt := time.Now().UTC().Add(5 * time.Minute)
			if sequenceRepo, ok := service.repo.(sequenceDeliveryRepository); ok {
				dueAt, dueErr := sequenceRepo.NextSequenceDueAt(ctx)
				if dueErr != nil {
					return summary, dueErr
				}
				if dueAt != nil && dueAt.Before(pollAt) {
					pollAt = dueAt.UTC()
					summary.StoppedReason = "waiting_for_followup"
				} else {
					summary.StoppedReason = "waiting_for_leads"
				}
			}
			summary.NextAvailableAt = &pollAt
		}
	}

	service.log.InfoContext(ctx, "bulk_outreach_completed",
		"attempted", summary.Attempted,
		"sent", summary.Sent,
		"skipped", summary.Skipped,
		"failed", summary.Failed,
		"triggered_by", triggeredBy,
		"stopped_reason", summary.StoppedReason,
	)
	if summary.NextAvailableAt == nil {
		_, _ = SetEmailJobControl(ctx, service.pool, false, nil)
	}

	return summary, nil
}

func (service *Service) loadBulkJobSummary(ctx context.Context, jobID uuid.UUID) (BulkSendSummary, error) {
	if service.pool == nil {
		return BulkSendSummary{}, fmt.Errorf("database pool is not configured")
	}
	var payload []byte
	if err := service.pool.QueryRow(ctx, `SELECT payload FROM job_runs WHERE id = $1`, jobID).Scan(&payload); err != nil {
		return BulkSendSummary{}, fmt.Errorf("load bulk outreach job summary: %w", err)
	}
	summary, err := decodeBulkSummary(payload)
	if err != nil {
		return BulkSendSummary{}, err
	}
	return summary, nil
}

func (service *Service) sendLead(ctx context.Context, lead EligibleLead, bulkJobID *uuid.UUID) (bool, error) {
	sequenceRepo, ok := service.repo.(sequenceDeliveryRepository)
	if !ok {
		return false, fmt.Errorf("sequence delivery repository is not configured")
	}
	if lead.Step < 1 {
		return false, fmt.Errorf("%w: sequence step must be 1-based", campaigns.ErrNotEligible)
	}
	delivery, err := sequenceRepo.GetSequenceDelivery(ctx, lead.CampaignID, lead.Step)
	if err != nil {
		return false, err
	}
	if delivery.RestaurantID != lead.RestaurantID {
		return false, fmt.Errorf("sequence campaign does not match the eligible lead")
	}
	if err := checkSequenceDeliveryEligibility(delivery); err != nil {
		return false, err
	}
	signature, err := service.activeSequenceSignature(ctx)
	if err != nil {
		return false, err
	}
	facts := delivery.GreetingFacts
	if facts.RestaurantName == "" {
		facts.RestaurantName = delivery.RestaurantName
	}
	if facts.OwnerFirstName == "" {
		facts.OwnerFirstName = delivery.OwnerFirstName
	}
	rendered, err := renderSequenceStep(delivery.Step, facts)
	if err != nil {
		return false, fmt.Errorf("render outreach sequence step: %w", err)
	}
	request := ensureSequenceSignature(emailprovider.SendRequest{
		To:       delivery.RecipientEmail,
		Subject:  rendered.Subject,
		TextBody: rendered.BodyText,
		Metadata: map[string]string{
			"campaign_id":   delivery.CampaignID.String(),
			"restaurant_id": delivery.RestaurantID.String(),
			"bulk_outreach": "true",
			"sequence_step": fmt.Sprintf("%d", delivery.Step.Position),
		},
		Delivery: &emailprovider.DeliveryContext{
			CampaignID:   delivery.CampaignID,
			RestaurantID: delivery.RestaurantID,
			BulkJobID:    bulkJobID,
			Step:         delivery.Step.Position,
		},
	}, signature)
	request.Delivery.CampaignArtifactFingerprint = emailprovider.CampaignArtifactFingerprint(
		request.Subject,
		request.HTMLBody,
		request.TextBody,
		"",
	)
	if err := sequenceRepo.PrepareSequenceDelivery(
		ctx,
		delivery.CampaignID,
		delivery.Step.Position,
		request.Subject,
		request.HTMLBody,
		request.TextBody,
	); err != nil {
		return false, err
	}

	result, err := service.emailPool.Send(ctx, request)
	if err != nil {
		if !result.QuotaManaged {
			finalizeErr := sequenceRepo.FinalizeSequenceDelivery(ctx, SequenceDeliveryFinalization{
				CampaignID:     delivery.CampaignID,
				RestaurantID:   delivery.RestaurantID,
				Step:           delivery.Step.Position,
				RecipientEmail: delivery.RecipientEmail,
				Outcome:        sequenceOutcomeUnknown,
				ErrorCode:      "provider_send_unknown",
			})
			if finalizeErr != nil {
				return false, fmt.Errorf("provider send failed: %v; finalize unknown sequence delivery: %w", err, finalizeErr)
			}
		}
		return false, err
	}

	if result.Skipped || result.RedirectedTo != "" {
		if result.QuotaManaged {
			if !result.Finalized {
				return false, fmt.Errorf("quota-managed skipped delivery was not finalized")
			}
			service.log.InfoContext(ctx, "bulk_outreach_lead_skipped",
				"restaurant_id", lead.RestaurantID,
				"campaign_id", delivery.CampaignID,
				"redirected", result.RedirectedTo != "",
				"account_key", result.AccountKey,
			)
			return false, nil
		}
		if err := sequenceRepo.FinalizeSequenceDelivery(ctx, SequenceDeliveryFinalization{
			CampaignID:     delivery.CampaignID,
			RestaurantID:   delivery.RestaurantID,
			Step:           delivery.Step.Position,
			RecipientEmail: delivery.RecipientEmail,
			Outcome:        sequenceOutcomeSkipped,
			Skipped:        result.Skipped,
			Redirected:     result.RedirectedTo != "",
		}); err != nil {
			return false, err
		}
		service.log.InfoContext(ctx, "bulk_outreach_lead_skipped",
			"restaurant_id", lead.RestaurantID,
			"campaign_id", delivery.CampaignID,
			"redirected", result.RedirectedTo != "",
		)
		return false, nil
	}
	if result.QuotaManaged {
		if !result.Finalized {
			return false, fmt.Errorf("quota-managed accepted delivery was not finalized")
		}
		if err := service.persistOutbound(
			ctx,
			delivery.RestaurantID,
			delivery.CampaignID,
			delivery.RecipientEmail,
			request.Subject,
			request.TextBody,
			request.HTMLBody,
			result,
		); err != nil {
			return false, err
		}
		service.log.InfoContext(ctx, "bulk_outreach_lead_sent",
			"restaurant_id", lead.RestaurantID,
			"campaign_id", delivery.CampaignID,
			"account_key", result.AccountKey,
			"account_sequence", result.AccountSequence,
			"send_sequence", result.SendSequence,
		)
		return true, nil
	}

	if err := sequenceRepo.FinalizeSequenceDelivery(ctx, SequenceDeliveryFinalization{
		CampaignID:        delivery.CampaignID,
		RestaurantID:      delivery.RestaurantID,
		Step:              delivery.Step.Position,
		RecipientEmail:    delivery.RecipientEmail,
		Outcome:           sequenceOutcomeSent,
		ProviderMessageID: result.ProviderMessageID,
	}); err != nil {
		return false, err
	}
	if err := service.persistOutbound(
		ctx,
		delivery.RestaurantID,
		delivery.CampaignID,
		delivery.RecipientEmail,
		request.Subject,
		request.TextBody,
		request.HTMLBody,
		result,
	); err != nil {
		return false, err
	}

	service.log.InfoContext(ctx, "bulk_outreach_lead_sent",
		"restaurant_id", lead.RestaurantID,
		"campaign_id", delivery.CampaignID,
	)
	return true, nil
}

func (service *Service) UpdateJobSummary(
	ctx context.Context,
	jobID string,
	lockedBy string,
	triggeredBy uuid.UUID,
	summary BulkSendSummary,
) error {
	payload, err := encodeBulkSummary(summary, triggeredBy.String())
	if err != nil {
		return err
	}
	const query = `
		UPDATE job_runs
		SET payload = $2, updated_at = now()
		WHERE id = $1::uuid
		  AND status = 'running'
		  AND locked_by = $3`
	result, err := service.pool.Exec(ctx, query, jobID, payload, lockedBy)
	if err != nil {
		return fmt.Errorf("update bulk job summary: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("bulk outreach job lease was lost before its summary could be updated")
	}
	return nil
}

const deferRunningBulkJobQuery = `
	UPDATE job_runs
	SET status = 'queued',
	    payload = $2,
	    available_at = $3,
	    attempts = 0,
	    locked_at = NULL,
	    locked_by = NULL,
	    lease_expires_at = NULL,
	    updated_at = now()
	WHERE id = $1::uuid
	  AND job_type = $4
	  AND status = 'running'
	  AND locked_by = $5`

func (service *Service) DeferBulkJob(
	ctx context.Context,
	jobID string,
	lockedBy string,
	triggeredBy uuid.UUID,
	summary BulkSendSummary,
) error {
	if summary.NextAvailableAt == nil {
		return fmt.Errorf("next available time is required to defer bulk outreach")
	}
	payload, err := encodeBulkSummary(summary, triggeredBy.String())
	if err != nil {
		return err
	}
	result, err := service.pool.Exec(
		ctx,
		deferRunningBulkJobQuery,
		jobID,
		payload,
		summary.NextAvailableAt.UTC(),
		BulkSendJobType,
		lockedBy,
	)
	if err != nil {
		return fmt.Errorf("defer bulk outreach job: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("bulk outreach job is not running and cannot be deferred")
	}
	return nil
}

func (service *Service) persistOutbound(
	ctx context.Context,
	restaurantID uuid.UUID,
	campaignID uuid.UUID,
	toEmail string,
	subject string,
	textBody string,
	htmlBody string,
	result emailprovider.SendResult,
) error {
	store, ok := service.repo.(*Postgres)
	if !ok || store == nil {
		return nil
	}
	now := time.Now().UTC()
	restaurantIDCopy := restaurantID
	campaignIDCopy := campaignID
	var attemptID *uuid.UUID
	if result.DeliveryAttemptID != uuid.Nil {
		id := result.DeliveryAttemptID
		attemptID = &id
	}
	replyToken := result.DeliveryAttemptID
	if parsed, ok := emailprovider.ParseReplyToken(
		result.ReplyTo,
		service.outreachCfg.InboundLocalPart,
		service.outreachCfg.InboundDomain,
	); ok {
		replyToken = parsed
	}
	var replyTokenPtr *uuid.UUID
	if replyToken != uuid.Nil {
		token := replyToken
		replyTokenPtr = &token
	}
	_, err := store.InsertMessage(ctx, Message{
		RestaurantID:      &restaurantIDCopy,
		CampaignID:        &campaignIDCopy,
		DeliveryAttemptID: attemptID,
		ReplyToken:        replyTokenPtr,
		Direction:         MessageDirectionOutbound,
		FromEmail:         result.FromEmail,
		ToEmail:           toEmail,
		ReplyTo:           result.ReplyTo,
		Subject:           subject,
		BodyText:          snapshotBody(textBody, htmlBody),
		GmailMessageID:    result.ProviderMessageID,
		GmailThreadID:     result.ProviderThreadID,
		RFCMessageID:      result.RFCMessageID,
		MailboxKey:        result.AccountKey,
		ReadAt:            &now,
	})
	if err != nil {
		return fmt.Errorf("record outbound email message: %w", err)
	}
	return nil
}

func (service *Service) ListInbox(
	ctx context.Context,
	principal auth.Principal,
	unreadOnly bool,
	restaurantOnly bool,
	mailboxKey string,
	limit int,
	offset int,
) (InboxList, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return InboxList{}, restaurants.ErrForbidden
	}
	store, ok := service.repo.(*Postgres)
	if !ok || store == nil {
		return InboxList{Threads: []InboxThread{}, Mailboxes: []InboxMailboxStatus{}}, nil
	}
	return store.ListInbox(ctx, unreadOnly, restaurantOnly, mailboxKey, limit, offset)
}

func (service *Service) ListRestaurantMessages(
	ctx context.Context,
	principal auth.Principal,
	restaurantID uuid.UUID,
	markRead bool,
) ([]Message, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return nil, restaurants.ErrForbidden
	}
	if _, err := service.restaurants.GetRestaurant(ctx, principal, restaurantID); err != nil {
		return nil, err
	}
	store, ok := service.repo.(*Postgres)
	if !ok || store == nil {
		return []Message{}, nil
	}
	if markRead {
		if err := store.MarkRestaurantInboundRead(ctx, restaurantID); err != nil {
			return nil, err
		}
	}
	return store.ListRestaurantMessages(ctx, restaurantID)
}

func (service *Service) MarkMessageRead(
	ctx context.Context,
	principal auth.Principal,
	messageID uuid.UUID,
) (Message, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return Message{}, restaurants.ErrForbidden
	}
	store, ok := service.repo.(*Postgres)
	if !ok || store == nil {
		return Message{}, repository.ErrNotFound
	}
	record, err := store.MarkMessageRead(ctx, messageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, repository.ErrNotFound
	}
	return record, err
}

func (service *Service) ReplyToInboxMessage(
	ctx context.Context,
	principal auth.Principal,
	messageID uuid.UUID,
	input ReplyMessageInput,
) (Message, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return Message{}, restaurants.ErrForbidden
	}
	if service.emailCfg.DisableSending {
		return Message{}, ErrSendingDisabled
	}
	store, ok := service.repo.(*Postgres)
	if !ok || store == nil {
		return Message{}, repository.ErrNotFound
	}
	target, err := store.GetMessageByID(ctx, messageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, repository.ErrNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("load inbox message for reply: %w", err)
	}
	request, err := service.prepareSignedInboxReply(ctx, target, input)
	if err != nil {
		return Message{}, err
	}
	mailboxKey := strings.TrimSpace(target.MailboxKey)
	if service.emailPool == nil || mailboxKey == "" {
		return Message{}, ErrInboxReplyUnavailable
	}

	result, err := service.emailPool.SendDirectFrom(ctx, mailboxKey, request)
	if err != nil {
		if errors.Is(err, emailprovider.ErrAccountsExhausted) || strings.Contains(err.Error(), "is not configured") {
			return Message{}, ErrInboxReplyUnavailable
		}
		return Message{}, fmt.Errorf("send inbox reply: %w", err)
	}

	now := time.Now().UTC()
	reply, err := store.InsertMessage(ctx, Message{
		RestaurantID:   target.RestaurantID,
		CampaignID:     target.CampaignID,
		ReplyToken:     target.ReplyToken,
		Direction:      MessageDirectionOutbound,
		FromEmail:      result.FromEmail,
		ToEmail:        request.To,
		Subject:        request.Subject,
		BodyText:       request.TextBody,
		GmailMessageID: result.ProviderMessageID,
		GmailThreadID:  firstNonEmpty(result.ProviderThreadID, target.GmailThreadID),
		RFCMessageID:   result.RFCMessageID,
		MailboxKey:     firstNonEmpty(result.AccountKey, mailboxKey),
		ReadAt:         &now,
	})
	if err != nil {
		return Message{}, fmt.Errorf("record accepted inbox reply: %w", err)
	}
	if _, err := store.MarkMessageRead(ctx, target.ID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		service.log.WarnContext(ctx, "outreach_inbox_reply_mark_read_failed", "message_id", target.ID, "error", err)
	}
	return reply, nil
}

func (service *Service) prepareSignedInboxReply(
	ctx context.Context,
	target Message,
	input ReplyMessageInput,
) (emailprovider.SendRequest, error) {
	request, err := prepareInboxReply(target, input)
	if err != nil {
		return emailprovider.SendRequest{}, err
	}
	signature, err := service.activeSequenceSignature(ctx)
	if err != nil {
		return emailprovider.SendRequest{}, err
	}
	return ensureSequenceSignature(request, signature), nil
}

func ensureSequenceSignature(request emailprovider.SendRequest, signature SequenceSignature) emailprovider.SendRequest {
	request.Signature = &emailprovider.SignatureDetails{
		Name:              signature.Name,
		Title:             signature.Title,
		AdditionalDetails: signature.AdditionalDetails,
	}
	return emailprovider.EnsureTuviSignature(request)
}
