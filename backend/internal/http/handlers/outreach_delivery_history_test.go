package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/auth"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/outreach"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/platform/config"
)

type deliveryHistoryHandlerRepo struct {
	result    outreach.DailyDeliveryList
	err       error
	accountID *uuid.UUID
	limit     int
	offset    int
}

func (*deliveryHistoryHandlerRepo) ListEligibleLeads(context.Context, int) ([]outreach.EligibleLead, error) {
	return nil, nil
}

func (*deliveryHistoryHandlerRepo) CountEligibleLeads(context.Context) (int, error) {
	return 0, nil
}

func (repo *deliveryHistoryHandlerRepo) ListDailyDeliveries(
	_ context.Context,
	_ time.Time,
	_ time.Time,
	accountID *uuid.UUID,
	limit int,
	offset int,
) (outreach.DailyDeliveryList, error) {
	repo.accountID = accountID
	repo.limit = limit
	repo.offset = offset
	return repo.result, repo.err
}

func newDeliveryHistoryHandler(repo *deliveryHistoryHandlerRepo) *OutreachBulkHandler {
	service := outreach.NewService(
		repo,
		nil,
		nil,
		nil,
		nil,
		outreach.DemoTokenResolver{},
		nil,
		nil,
		config.EmailConfig{},
		config.OutreachConfig{},
		config.AppURLsConfig{},
		nil,
		nil,
	)
	return NewOutreachBulkHandler(
		service,
		func(w http.ResponseWriter, status int, value any) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(value)
		},
		func(w http.ResponseWriter, status int, code string, message string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": code, "message": message},
			})
		},
	)
}

func deliveryHistoryRequest(path string, role string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	return request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{
		UserID: uuid.New(),
		Role:   role,
	}))
}

func TestListDeliveriesReturnsFilteredDailyLedger(t *testing.T) {
	accountID := uuid.New()
	restaurantID := uuid.New()
	attemptID := uuid.New()
	now := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	repo := &deliveryHistoryHandlerRepo{result: outreach.DailyDeliveryList{
		Summary: outreach.DeliveryOutcomeCounts{Total: 1, Sent: 1},
		Senders: []outreach.DailyDeliverySender{{
			AccountID: accountID, AccountKey: "contact", SenderEmail: "contact@tuvisolutions.com",
			Counts: outreach.DeliveryOutcomeCounts{Total: 1, Sent: 1}, Phase2Sent: 1,
		}},
		Deliveries: []outreach.DailyDelivery{{
			ID: attemptID, RestaurantID: restaurantID, RestaurantName: "Example Restaurant",
			RecipientEmail: "owner@example.com", AccountID: accountID, AccountKey: "contact",
			SenderEmail: "contact@tuvisolutions.com", CampaignStep: 2, Status: "sent",
			Outcome: "Provider accepted", Subject: "A quick question", AttemptedAt: now, OutcomeAt: &now,
		}},
		Total: 1,
	}}
	handler := newDeliveryHistoryHandler(repo)
	request := deliveryHistoryRequest(
		"/api/v1/outreach/deliveries?date=2026-08-20&account_id="+accountID.String()+"&limit=25&offset=50",
		auth.RoleInternalAdmin,
	)
	response := httptest.NewRecorder()

	handler.ListDeliveries(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	if repo.accountID == nil || *repo.accountID != accountID || repo.limit != 25 || repo.offset != 50 {
		t.Fatalf("repository filters = %#v", repo)
	}
	var body outreach.DailyDeliveryList
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Date != "2026-08-20" || body.Timezone != "Australia/Sydney" || len(body.Deliveries) != 1 {
		t.Fatalf("response = %#v", body)
	}
	if body.Deliveries[0].RecipientEmail != "owner@example.com" || body.Deliveries[0].Outcome != "Provider accepted" {
		t.Fatalf("delivery = %#v", body.Deliveries[0])
	}
	if len(body.Senders) != 1 || body.Senders[0].Phase1Sent != 0 || body.Senders[0].Phase2Sent != 1 ||
		body.Senders[0].Phase3Sent != 0 || body.Senders[0].OtherSent != 0 {
		t.Fatalf("sender phase counts = %#v", body.Senders)
	}
}

func TestListDeliveriesRejectsInvalidOrUnauthorizedQueries(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		role       string
		principal  bool
		wantStatus int
	}{
		{name: "authentication required", path: "/api/v1/outreach/deliveries?date=2026-08-20", principal: false, wantStatus: http.StatusUnauthorized},
		{name: "internal admin required", path: "/api/v1/outreach/deliveries?date=2026-08-20", role: auth.RoleRestaurantOwner, principal: true, wantStatus: http.StatusForbidden},
		{name: "date required", path: "/api/v1/outreach/deliveries", role: auth.RoleInternalAdmin, principal: true, wantStatus: http.StatusBadRequest},
		{name: "valid account uuid", path: "/api/v1/outreach/deliveries?date=2026-08-20&account_id=nope", role: auth.RoleInternalAdmin, principal: true, wantStatus: http.StatusBadRequest},
		{name: "bounded limit", path: "/api/v1/outreach/deliveries?date=2026-08-20&limit=101", role: auth.RoleInternalAdmin, principal: true, wantStatus: http.StatusBadRequest},
		{name: "nonnegative offset", path: "/api/v1/outreach/deliveries?date=2026-08-20&offset=-1", role: auth.RoleInternalAdmin, principal: true, wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newDeliveryHistoryHandler(&deliveryHistoryHandlerRepo{})
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.principal {
				request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: uuid.New(), Role: test.role}))
			}
			response := httptest.NewRecorder()
			handler.ListDeliveries(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestListDeliveriesRedactsRepositoryFailure(t *testing.T) {
	handler := newDeliveryHistoryHandler(&deliveryHistoryHandlerRepo{err: errors.New("secret database detail")})
	response := httptest.NewRecorder()
	handler.ListDeliveries(response, deliveryHistoryRequest(
		"/api/v1/outreach/deliveries?date=2026-08-20",
		auth.RoleInternalAdmin,
	))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret database detail") ||
		!strings.Contains(response.Body.String(), "Scheduled outreach delivery history is unavailable") {
		t.Fatalf("response must contain only the safe error message: %s", response.Body.String())
	}
}

func TestDeliveryHistoryOpenAPIContract(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return this test file")
	}
	openAPIPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "..", "docs", "openapi", "openapi.yaml")
	openAPI, err := os.ReadFile(openAPIPath)
	if err != nil {
		t.Fatalf("read OpenAPI contract: %v", err)
	}
	contract := string(openAPI)
	for _, required := range []string{
		"/api/v1/outreach/deliveries:",
		"operationId: listDailyOutreachDeliveries",
		"$ref: '#/components/schemas/DailyOutreachDeliveryList'",
		"enum: [sending, sent, skipped, failed, unknown]",
		"timezone:",
		"provider_message_id:",
		"phase_1_sent:",
		"phase_2_sent:",
		"phase_3_sent:",
		"other_sent:",
	} {
		if !strings.Contains(contract, required) {
			t.Fatalf("OpenAPI delivery history contract missing %q", required)
		}
	}
}
