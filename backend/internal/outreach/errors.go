package outreach

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const BulkSendJobType = "outreach.bulk_send"

type BulkJobEnqueuer interface {
	EnqueueBulkSend(ctx context.Context, triggeredBy uuid.UUID) (jobID string, err error)
}

var (
	ErrBulkJobActive         = errors.New("a bulk outreach job is already queued or running")
	ErrNotConfigured         = errors.New("bulk outreach email accounts are not configured")
	ErrSendingDisabled       = errors.New("email sending is disabled")
	ErrInvalidRecipientEmail = errors.New("recipient email is invalid")
	ErrInvalidInboxReply     = errors.New("inbox reply is invalid")
	ErrInboxReplyUnavailable = errors.New("inbox reply mailbox is not configured for sending")
	ErrInvalidSendSchedule   = errors.New("outreach send schedule is invalid")
	ErrSendScheduleLocked    = errors.New("outreach send schedule cannot change while the email job is enabled or active")
	ErrInvalidDeliveryQuery  = errors.New("outreach delivery history query is invalid")
)

type BulkSendSummary struct {
	Attempted       int        `json:"attempted"`
	Sent            int        `json:"sent"`
	Failed          int        `json:"failed"`
	Skipped         int        `json:"skipped"`
	MaxSends        int        `json:"max_sends"`
	StoppedReason   string     `json:"stopped_reason,omitempty"`
	NextAvailableAt *time.Time `json:"next_available_at,omitempty"`
}

type EmailJobControl struct {
	Enabled   bool       `json:"enabled"`
	EnabledAt *time.Time `json:"enabled_at,omitempty"`
	EnabledBy *uuid.UUID `json:"enabled_by,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type EmailJobActionResult struct {
	EmailJob             EmailJobControl `json:"email_job"`
	JobID                string          `json:"job_id,omitempty"`
	Status               string          `json:"status"`
	MaxSends             int             `json:"max_sends"`
	PendingEligibleCount int             `json:"pending_eligible_count"`
}

type StatusResult struct {
	PendingEligibleCount    int                 `json:"pending_eligible_count"`
	DueFollowupCount        int                 `json:"due_followup_count"`
	NewRecipientCount       int                 `json:"new_recipient_count"`
	PausedRecipientCount    int                 `json:"paused_recipient_count"`
	CompletedRecipientCount int                 `json:"completed_recipient_count"`
	SentCounts              SentCounts          `json:"sent_counts"`
	MaxSends                int                 `json:"max_sends"`
	ActiveJob               *ActiveJobStatus    `json:"active_job,omitempty"`
	LastCompletedJob        *CompletedJobStatus `json:"last_completed_job,omitempty"`
	NextAvailableAt         *time.Time          `json:"next_available_at,omitempty"`
	EmailJob                EmailJobControl     `json:"email_job"`
	SendSchedule            EmailSendSchedule   `json:"send_schedule"`
}

type SentCounts struct {
	Total  int `json:"total"`
	Phase1 int `json:"phase_1"`
	Phase2 int `json:"phase_2"`
	Phase3 int `json:"phase_3"`
	Other  int `json:"other"`
}

type RecipientStatusCounts struct {
	DueFollowups  int
	NewRecipients int
	Paused        int
	Completed     int
}

type ActiveJobStatus struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

type CompletedJobStatus struct {
	JobID   string          `json:"job_id"`
	Status  string          `json:"status"`
	Summary BulkSendSummary `json:"summary"`
}

func validateBulkMax(max int) error {
	if max < 1 || max > 150 {
		return fmt.Errorf("bulk max sends must be between 1 and 150")
	}
	return nil
}
