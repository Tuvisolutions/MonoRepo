package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/auth"
)

func TestOutreachInboxHandlersValidateAuthAndIdentifiersBeforeService(t *testing.T) {
	t.Parallel()

	handler := NewOutreachBulkHandler(
		nil,
		func(w http.ResponseWriter, status int, value any) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(value)
		},
		func(w http.ResponseWriter, status int, code string, message string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
		},
	)
	admin := auth.Principal{UserID: uuid.New(), Role: auth.RoleInternalAdmin}

	tests := []struct {
		name       string
		request    *http.Request
		serve      func(http.ResponseWriter, *http.Request)
		principal  *auth.Principal
		wantStatus int
	}{
		{
			name:       "inbox requires authentication",
			request:    httptest.NewRequest(http.MethodGet, "/api/v1/outreach/inbox", nil),
			serve:      handler.ListInbox,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "inbox rejects a limit above contract maximum",
			request:    httptest.NewRequest(http.MethodGet, "/api/v1/outreach/inbox?limit=101", nil),
			serve:      handler.ListInbox,
			principal:  &admin,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "inbox rejects a multiline mailbox key",
			request:    httptest.NewRequest(http.MethodGet, "/api/v1/outreach/inbox?mailbox=sales%0Aother", nil),
			serve:      handler.ListInbox,
			principal:  &admin,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "inbox rejects a multiline search",
			request:    httptest.NewRequest(http.MethodGet, "/api/v1/outreach/inbox?q=owner%0Aother", nil),
			serve:      handler.ListInbox,
			principal:  &admin,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "shared emails rejects a negative offset",
			request:    httptest.NewRequest(http.MethodGet, "/api/v1/restaurants/shared-emails?offset=-1", nil),
			serve:      handler.ListSharedEmailGroups,
			principal:  &admin,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "restaurant messages reject invalid restaurant id",
			request:    requestWithPathValue(http.MethodGet, "/api/v1/restaurants/not-a-uuid/messages", "id", "not-a-uuid"),
			serve:      handler.ListRestaurantMessages,
			principal:  &admin,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "restaurant greeting rejects invalid restaurant id",
			request:    requestWithPathValue(http.MethodGet, "/api/v1/restaurants/not-a-uuid/outreach-greeting", "id", "not-a-uuid"),
			serve:      handler.PreviewRestaurantGreeting,
			principal:  &admin,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "mark read rejects invalid message id",
			request:    requestWithPathValue(http.MethodPost, "/api/v1/outreach/messages/not-a-uuid/read", "id", "not-a-uuid"),
			serve:      handler.MarkMessageRead,
			principal:  &admin,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "reply rejects invalid message id",
			request:    requestWithPathValue(http.MethodPost, "/api/v1/outreach/messages/not-a-uuid/reply", "id", "not-a-uuid"),
			serve:      handler.ReplyToInboxMessage,
			principal:  &admin,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "sequence preview rejects invalid restaurant id",
			request: requestWithPathValue(
				http.MethodPost,
				"/api/v1/outreach/sequences/"+uuid.NewString()+"/preview",
				"id",
				uuid.NewString(),
			),
			serve: func(w http.ResponseWriter, r *http.Request) {
				r.Body = io.NopCloser(strings.NewReader(`{"restaurant_id":"not-a-uuid"}`))
				handler.PreviewSequence(w, r)
			},
			principal:  &admin,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "template test rejects invalid restaurant id",
			request:    httptest.NewRequest(http.MethodPost, "/api/v1/outreach/test-send", strings.NewReader(`{"recipient_email":"test@example.com","restaurant_id":"not-a-uuid"}`)),
			serve:      handler.SendTemplateTest,
			principal:  &admin,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := test.request
			if test.principal != nil {
				request = request.WithContext(auth.WithPrincipal(request.Context(), *test.principal))
			}
			response := httptest.NewRecorder()
			test.serve(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func requestWithPathValue(method, target, key, value string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.SetPathValue(key, value)
	return request
}
