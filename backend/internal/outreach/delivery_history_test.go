package outreach

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/auth"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/restaurants"
)

type recordingDailyDeliveryRepo struct {
	result    DailyDeliveryList
	err       error
	calls     int
	start     time.Time
	end       time.Time
	accountID *uuid.UUID
	limit     int
	offset    int
}

func (*recordingDailyDeliveryRepo) ListEligibleLeads(context.Context, int) ([]EligibleLead, error) {
	return nil, nil
}

func (*recordingDailyDeliveryRepo) CountEligibleLeads(context.Context) (int, error) {
	return 0, nil
}

func (repo *recordingDailyDeliveryRepo) ListDailyDeliveries(
	_ context.Context,
	start time.Time,
	end time.Time,
	accountID *uuid.UUID,
	limit int,
	offset int,
) (DailyDeliveryList, error) {
	repo.calls++
	repo.start = start
	repo.end = end
	repo.accountID = accountID
	repo.limit = limit
	repo.offset = offset
	return repo.result, repo.err
}

func TestDailyDeliveryBoundsUsesSydneyCalendarAcrossDST(t *testing.T) {
	tests := []struct {
		date     string
		duration time.Duration
	}{
		{date: "2026-02-10", duration: 24 * time.Hour},
		{date: "2026-04-05", duration: 25 * time.Hour},
		{date: "2026-10-04", duration: 23 * time.Hour},
	}
	for _, test := range tests {
		t.Run(test.date, func(t *testing.T) {
			canonical, start, end, err := dailyDeliveryBounds(test.date)
			if err != nil {
				t.Fatalf("dailyDeliveryBounds() error = %v", err)
			}
			if canonical != test.date || end.Sub(start) != test.duration {
				t.Fatalf("bounds = %q %s..%s (%s), want %s", canonical, start, end, end.Sub(start), test.duration)
			}
		})
	}
}

func TestDailyDeliveryBoundsRejectsInvalidDate(t *testing.T) {
	for _, date := range []string{"", "2026-2-01", "2026-02-30", "2026/02/01"} {
		if _, _, _, err := dailyDeliveryBounds(date); !errors.Is(err, ErrInvalidDeliveryQuery) {
			t.Fatalf("dailyDeliveryBounds(%q) error = %v, want invalid query", date, err)
		}
	}
}

func TestListDailyDeliveriesUsesAuthorizedSydneyBoundsAndPagination(t *testing.T) {
	accountID := uuid.New()
	repo := &recordingDailyDeliveryRepo{result: DailyDeliveryList{
		Summary: DeliveryOutcomeCounts{Total: 1, Sent: 1},
	}}
	service := &Service{repo: repo}
	principal := auth.Principal{UserID: uuid.New(), Role: auth.RoleInternalAdmin}

	result, err := service.ListDailyDeliveries(context.Background(), principal, DailyDeliveryQuery{
		Date: "2026-08-20", AccountID: &accountID, Limit: 25, Offset: 50,
	})
	if err != nil {
		t.Fatalf("ListDailyDeliveries() error = %v", err)
	}
	if repo.calls != 1 || repo.accountID == nil || *repo.accountID != accountID || repo.limit != 25 || repo.offset != 50 {
		t.Fatalf("repository call = %#v", repo)
	}
	if result.Date != "2026-08-20" || result.Timezone != scheduledSendTimezone || result.Limit != 25 || result.Offset != 50 {
		t.Fatalf("result metadata = %#v", result)
	}
	if result.Senders == nil || result.Deliveries == nil {
		t.Fatalf("result collections must be non-nil: %#v", result)
	}
	location, err := time.LoadLocation(scheduledSendTimezone)
	if err != nil {
		t.Fatalf("load Sydney timezone: %v", err)
	}
	if repo.start.In(location).Format("2006-01-02 15:04") != "2026-08-20 00:00" ||
		repo.end.In(location).Format("2006-01-02 15:04") != "2026-08-21 00:00" {
		t.Fatalf("repository Sydney bounds = %s..%s", repo.start.In(location), repo.end.In(location))
	}
}

func TestListDailyDeliveriesRejectsNonAdminBeforeRepository(t *testing.T) {
	repo := &recordingDailyDeliveryRepo{}
	service := &Service{repo: repo}
	_, err := service.ListDailyDeliveries(context.Background(), auth.Principal{Role: auth.RoleRestaurantOwner}, DailyDeliveryQuery{
		Date: "2026-08-20", Limit: 50,
	})
	if !errors.Is(err, restaurants.ErrForbidden) || repo.calls != 0 {
		t.Fatalf("error/calls = %v/%d, want forbidden before repository", err, repo.calls)
	}
}

func TestListDailyDeliveriesDefaultsPageSize(t *testing.T) {
	repo := &recordingDailyDeliveryRepo{}
	service := &Service{repo: repo}
	_, err := service.ListDailyDeliveries(
		context.Background(),
		auth.Principal{UserID: uuid.New(), Role: auth.RoleInternalAdmin},
		DailyDeliveryQuery{Date: "2026-08-20"},
	)
	if err != nil {
		t.Fatalf("ListDailyDeliveries() error = %v", err)
	}
	if repo.limit != defaultDeliveryPageSize || repo.offset != 0 {
		t.Fatalf("repository pagination = limit %d offset %d, want %d/0", repo.limit, repo.offset, defaultDeliveryPageSize)
	}
}

func TestDeliveryOutcomeLabelDistinguishesAcceptanceAndBounce(t *testing.T) {
	tests := []struct {
		status string
		code   string
		want   string
	}{
		{status: "sent", want: "Provider accepted"},
		{status: "failed", code: "gmail_sender_rate_limit_bounce", want: "Bounced — sender rate limit"},
		{status: "failed", code: "gmail_rate_limit_rejected", want: "Rate limited — not sent"},
		{status: "failed", code: "gmail_pre_send_unavailable", want: "Gmail unavailable — not sent"},
		{status: "failed", code: "credential_or_authorization_rejected", want: "Rejected before send"},
		{status: "failed", code: "provider_rejected_before_acceptance", want: "Rejected before send"},
		{status: "sent", code: "gmail_sender_rate_limit_bounce", want: "Provider accepted"},
		{status: "failed", want: "Failed"},
		{status: "unknown", want: "Outcome unknown"},
		{status: "skipped", want: "Skipped"},
		{status: "sending", want: "In progress"},
	}
	for _, test := range tests {
		if got := deliveryOutcomeLabel(test.status, test.code); got != test.want {
			t.Fatalf("deliveryOutcomeLabel(%q, %q) = %q, want %q", test.status, test.code, got, test.want)
		}
	}
}

func TestDailyDeliveryQueriesUseLedgerDayAndSafeSnapshots(t *testing.T) {
	for _, required := range []string{
		"attempt.created_at >= $1",
		"attempt.created_at < $2",
		"LEFT JOIN email_messages AS message",
		"message.direction = 'outbound'",
		"($3::uuid IS NULL OR attempt.account_id = $3)",
		"COALESCE(message.subject, '')",
		"ORDER BY attempt.created_at DESC, attempt.send_sequence DESC",
		"LIMIT $4 OFFSET $5",
	} {
		if !strings.Contains(dailyDeliveriesQuery, required) {
			t.Fatalf("daily delivery query missing %q", required)
		}
	}
	if strings.Contains(dailyDeliveriesQuery, "WHERE attempt.status = 'sent'") {
		t.Fatal("daily delivery query must retain non-sent outcomes")
	}
	if strings.Contains(dailyDeliveriesQuery, "ELSE lower(trim(account.provider_identity))") {
		t.Fatal("daily delivery query must not expose an opaque non-Gmail provider identity as an email address")
	}
	if !strings.Contains(dailyDeliveriesQuery, "WHEN attempt.status = 'sending' THEN NULL") {
		t.Fatal("in-progress delivery rows must not report a completed outcome timestamp")
	}
	for _, required := range []string{
		"LEFT JOIN email_delivery_attempts AS attempt",
		"attempt.created_at >= $1",
		"attempt.created_at < $2",
		"FILTER (WHERE attempt.status = 'unknown')",
	} {
		if !strings.Contains(dailyDeliverySendersQuery, required) {
			t.Fatalf("daily sender query missing %q", required)
		}
	}
}
