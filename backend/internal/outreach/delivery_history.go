package outreach

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/auth"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/restaurants"
)

const (
	deliveryHistoryDateLayout = "2006-01-02"
	defaultDeliveryPageSize   = 50
	maximumDeliveryPageSize   = 100
)

type DailyDeliveryQuery struct {
	Date      string
	AccountID *uuid.UUID
	Limit     int
	Offset    int
}

type DeliveryOutcomeCounts struct {
	Total   int `json:"total"`
	Sent    int `json:"sent"`
	Failed  int `json:"failed"`
	Unknown int `json:"unknown"`
	Skipped int `json:"skipped"`
	Sending int `json:"sending"`
}

type DailyDeliverySender struct {
	AccountID   uuid.UUID             `json:"account_id"`
	AccountKey  string                `json:"account_key"`
	SenderEmail string                `json:"sender_email"`
	Counts      DeliveryOutcomeCounts `json:"counts"`
}

type DailyDelivery struct {
	ID                uuid.UUID  `json:"id"`
	RestaurantID      uuid.UUID  `json:"restaurant_id"`
	RestaurantName    string     `json:"restaurant_name"`
	RecipientEmail    string     `json:"recipient_email"`
	AccountID         uuid.UUID  `json:"account_id"`
	AccountKey        string     `json:"account_key"`
	SenderEmail       string     `json:"sender_email"`
	CampaignStep      int        `json:"campaign_step"`
	Status            string     `json:"status"`
	Outcome           string     `json:"outcome"`
	ErrorCode         string     `json:"error_code,omitempty"`
	Subject           string     `json:"subject,omitempty"`
	ProviderMessageID string     `json:"provider_message_id,omitempty"`
	AttemptedAt       time.Time  `json:"attempted_at"`
	OutcomeAt         *time.Time `json:"outcome_at,omitempty"`
	SentAt            *time.Time `json:"sent_at,omitempty"`
}

type DailyDeliveryList struct {
	Date       string                `json:"date"`
	Timezone   string                `json:"timezone"`
	Summary    DeliveryOutcomeCounts `json:"summary"`
	Senders    []DailyDeliverySender `json:"senders"`
	Deliveries []DailyDelivery       `json:"deliveries"`
	Total      int                   `json:"total"`
	Limit      int                   `json:"limit"`
	Offset     int                   `json:"offset"`
}

type dailyDeliveryRepository interface {
	ListDailyDeliveries(
		ctx context.Context,
		start time.Time,
		end time.Time,
		accountID *uuid.UUID,
		limit int,
		offset int,
	) (DailyDeliveryList, error)
}

const dailyDeliverySendersQuery = `
	SELECT account.id,
	       account.account_key,
	       CASE
	         WHEN trim(account.from_email) <> '' THEN lower(trim(account.from_email))
	         WHEN account.provider = 'gmail' THEN lower(trim(split_part(account.provider_identity, '|', 2)))
	         ELSE ''
	       END AS from_email,
	       count(attempt.id)::int AS attempts,
	       (count(attempt.id) FILTER (WHERE attempt.status = 'sent'))::int AS sent,
	       (count(attempt.id) FILTER (WHERE attempt.status = 'failed'))::int AS failed,
	       (count(attempt.id) FILTER (WHERE attempt.status = 'unknown'))::int AS unknown,
	       (count(attempt.id) FILTER (WHERE attempt.status = 'skipped'))::int AS skipped,
	       (count(attempt.id) FILTER (WHERE attempt.status = 'sending'))::int AS sending
	FROM outreach_email_accounts AS account
	LEFT JOIN email_delivery_attempts AS attempt
	  ON attempt.account_id = account.id
	 AND attempt.created_at >= $1
	 AND attempt.created_at < $2
	GROUP BY account.position, account.id, account.account_key,
	         account.from_email, account.provider, account.provider_identity
	ORDER BY account.position, account.account_key`

const dailyDeliveriesQuery = `
	SELECT attempt.id,
	       attempt.restaurant_id,
	       restaurant.name,
	       attempt.recipient_email,
	       account.id,
	       account.account_key,
	       CASE
	         WHEN trim(message.from_email) <> '' THEN lower(trim(message.from_email))
	         WHEN trim(account.from_email) <> '' THEN lower(trim(account.from_email))
	         WHEN account.provider = 'gmail' THEN lower(trim(split_part(account.provider_identity, '|', 2)))
	         ELSE ''
	       END AS sender_email,
	       attempt.campaign_step,
	       attempt.status,
	       attempt.error_code,
	       COALESCE(message.subject, ''),
	       attempt.provider_message_id,
	       attempt.created_at,
	       CASE
	         WHEN attempt.status = 'sending' THEN NULL
	         WHEN attempt.status = 'sent' THEN COALESCE(attempt.sent_at, attempt.updated_at)
	         ELSE attempt.updated_at
	       END AS outcome_at,
	       attempt.sent_at
	FROM email_delivery_attempts AS attempt
	JOIN outreach_email_accounts AS account ON account.id = attempt.account_id
	JOIN restaurants AS restaurant ON restaurant.id = attempt.restaurant_id
	LEFT JOIN email_messages AS message
	  ON message.delivery_attempt_id = attempt.id
	 AND message.direction = 'outbound'
	WHERE attempt.created_at >= $1
	  AND attempt.created_at < $2
	  AND ($3::uuid IS NULL OR attempt.account_id = $3)
	ORDER BY attempt.created_at DESC, attempt.send_sequence DESC
	LIMIT $4 OFFSET $5`

func (repo *Postgres) ListDailyDeliveries(
	ctx context.Context,
	start time.Time,
	end time.Time,
	accountID *uuid.UUID,
	limit int,
	offset int,
) (DailyDeliveryList, error) {
	if repo.pool == nil {
		return DailyDeliveryList{}, fmt.Errorf("database pool is not configured")
	}
	tx, err := repo.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return DailyDeliveryList{}, fmt.Errorf("begin daily outreach delivery snapshot: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	result := DailyDeliveryList{
		Senders:    []DailyDeliverySender{},
		Deliveries: []DailyDelivery{},
		Limit:      limit,
		Offset:     offset,
	}
	senderRows, err := tx.Query(ctx, dailyDeliverySendersQuery, start, end)
	if err != nil {
		return DailyDeliveryList{}, fmt.Errorf("list daily outreach sender totals: %w", err)
	}
	for senderRows.Next() {
		var sender DailyDeliverySender
		if err := senderRows.Scan(
			&sender.AccountID,
			&sender.AccountKey,
			&sender.SenderEmail,
			&sender.Counts.Total,
			&sender.Counts.Sent,
			&sender.Counts.Failed,
			&sender.Counts.Unknown,
			&sender.Counts.Skipped,
			&sender.Counts.Sending,
		); err != nil {
			senderRows.Close()
			return DailyDeliveryList{}, fmt.Errorf("scan daily outreach sender totals: %w", err)
		}
		result.Senders = append(result.Senders, sender)
		if accountID == nil || sender.AccountID == *accountID {
			result.Summary.Total += sender.Counts.Total
			result.Summary.Sent += sender.Counts.Sent
			result.Summary.Failed += sender.Counts.Failed
			result.Summary.Unknown += sender.Counts.Unknown
			result.Summary.Skipped += sender.Counts.Skipped
			result.Summary.Sending += sender.Counts.Sending
		}
	}
	if err := senderRows.Err(); err != nil {
		senderRows.Close()
		return DailyDeliveryList{}, fmt.Errorf("iterate daily outreach sender totals: %w", err)
	}
	senderRows.Close()
	result.Total = result.Summary.Total

	var accountFilter any
	if accountID != nil {
		accountFilter = *accountID
	}
	rows, err := tx.Query(ctx, dailyDeliveriesQuery, start, end, accountFilter, limit, offset)
	if err != nil {
		return DailyDeliveryList{}, fmt.Errorf("list daily outreach deliveries: %w", err)
	}
	for rows.Next() {
		var item DailyDelivery
		if err := rows.Scan(
			&item.ID,
			&item.RestaurantID,
			&item.RestaurantName,
			&item.RecipientEmail,
			&item.AccountID,
			&item.AccountKey,
			&item.SenderEmail,
			&item.CampaignStep,
			&item.Status,
			&item.ErrorCode,
			&item.Subject,
			&item.ProviderMessageID,
			&item.AttemptedAt,
			&item.OutcomeAt,
			&item.SentAt,
		); err != nil {
			rows.Close()
			return DailyDeliveryList{}, fmt.Errorf("scan daily outreach delivery: %w", err)
		}
		item.Outcome = deliveryOutcomeLabel(item.Status, item.ErrorCode)
		result.Deliveries = append(result.Deliveries, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return DailyDeliveryList{}, fmt.Errorf("iterate daily outreach deliveries: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return DailyDeliveryList{}, fmt.Errorf("commit daily outreach delivery snapshot: %w", err)
	}
	return result, nil
}

func (service *Service) ListDailyDeliveries(
	ctx context.Context,
	principal auth.Principal,
	query DailyDeliveryQuery,
) (DailyDeliveryList, error) {
	if !auth.IsInternalAdmin(principal.Role) {
		return DailyDeliveryList{}, restaurants.ErrForbidden
	}
	canonicalDate, start, end, err := dailyDeliveryBounds(query.Date)
	if err != nil {
		return DailyDeliveryList{}, err
	}
	if query.Limit == 0 {
		query.Limit = defaultDeliveryPageSize
	}
	if query.Limit < 1 || query.Limit > maximumDeliveryPageSize {
		return DailyDeliveryList{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidDeliveryQuery, maximumDeliveryPageSize)
	}
	if query.Offset < 0 {
		return DailyDeliveryList{}, fmt.Errorf("%w: offset must be a non-negative integer", ErrInvalidDeliveryQuery)
	}
	repo, ok := service.repo.(dailyDeliveryRepository)
	if !ok {
		return DailyDeliveryList{}, fmt.Errorf("daily outreach delivery repository is not configured")
	}
	result, err := repo.ListDailyDeliveries(ctx, start, end, query.AccountID, query.Limit, query.Offset)
	if err != nil {
		return DailyDeliveryList{}, err
	}
	result.Date = canonicalDate
	result.Timezone = scheduledSendTimezone
	result.Limit = query.Limit
	result.Offset = query.Offset
	if result.Senders == nil {
		result.Senders = []DailyDeliverySender{}
	}
	if result.Deliveries == nil {
		result.Deliveries = []DailyDelivery{}
	}
	return result, nil
}

func deliveryOutcomeLabel(status, errorCode string) string {
	normalizedStatus := strings.ToLower(strings.TrimSpace(status))
	if normalizedStatus == "failed" {
		if strings.EqualFold(strings.TrimSpace(errorCode), "gmail_sender_rate_limit_bounce") {
			return "Bounced — sender rate limit"
		}
		if strings.EqualFold(strings.TrimSpace(errorCode), "credential_or_authorization_rejected") ||
			strings.EqualFold(strings.TrimSpace(errorCode), "provider_rejected_before_acceptance") {
			return "Rejected before send"
		}
	}
	switch normalizedStatus {
	case "sent":
		return "Provider accepted"
	case "failed":
		return "Failed"
	case "unknown":
		return "Outcome unknown"
	case "skipped":
		return "Skipped"
	case "sending":
		return "In progress"
	default:
		return "Unknown status"
	}
}

func dailyDeliveryBounds(rawDate string) (string, time.Time, time.Time, error) {
	date := strings.TrimSpace(rawDate)
	location, err := time.LoadLocation(scheduledSendTimezone)
	if err != nil {
		return "", time.Time{}, time.Time{}, fmt.Errorf("load outreach delivery timezone: %w", err)
	}
	start, err := time.ParseInLocation(deliveryHistoryDateLayout, date, location)
	if err != nil || start.Format(deliveryHistoryDateLayout) != date {
		return "", time.Time{}, time.Time{}, fmt.Errorf("%w: date must use YYYY-MM-DD in Australia/Sydney", ErrInvalidDeliveryQuery)
	}
	end := start.AddDate(0, 0, 1)
	return date, start.UTC(), end.UTC(), nil
}
