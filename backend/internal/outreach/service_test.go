package outreach_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/auth"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/campaigns"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/outreach"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/platform/config"
	repository "github.com/rajchodisetti/restaurant-platform/backend/internal/platform/errors"
	emailprovider "github.com/rajchodisetti/restaurant-platform/backend/internal/providers/email"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/restaurants"
)

type mockRepo struct {
	count              int
	leads              []outreach.EligibleLead
	activeSteps        []outreach.SequenceStep
	sequenceSteps      []outreach.SequenceStep
	signatures         map[uuid.UUID]outreach.SequenceSignature
	activeSignature    *outreach.SequenceSignature
	activeSignatureErr error
	greetingFacts      map[uuid.UUID]outreach.GreetingFacts
	greetingErr        error
	delivery           outreach.SequenceDelivery
	prepared           outreach.RenderedSequenceStep
	preparedHTML       string
	finalizations      []outreach.SequenceDeliveryFinalization
	nextDue            *time.Time
}

type statusCountsRepo struct {
	*mockRepo
	sentCounts outreach.SentCounts
	sentErr    error
}

func (repo *statusCountsRepo) CountSentDeliveriesByPhase(context.Context) (outreach.SentCounts, error) {
	return repo.sentCounts, repo.sentErr
}

func (repo *mockRepo) ListEligibleLeads(context.Context, int) ([]outreach.EligibleLead, error) {
	return repo.leads, nil
}

func (repo *mockRepo) CountEligibleLeads(context.Context) (int, error) { return repo.count, nil }

func (repo *mockRepo) ListActiveSequenceSteps(context.Context) ([]outreach.SequenceStep, error) {
	return repo.activeSteps, nil
}

func (repo *mockRepo) ListSequenceSteps(context.Context, uuid.UUID) ([]outreach.SequenceStep, error) {
	if repo.sequenceSteps != nil {
		return repo.sequenceSteps, nil
	}
	return repo.activeSteps, nil
}

func (repo *mockRepo) GetSequenceSignature(_ context.Context, sequenceID uuid.UUID) (outreach.SequenceSignature, error) {
	if signature, ok := repo.signatures[sequenceID]; ok {
		return signature, nil
	}
	return outreach.DefaultSequenceSignature(), nil
}

func (repo *mockRepo) GetActiveSequenceSignature(context.Context) (outreach.SequenceSignature, error) {
	if repo.activeSignatureErr != nil {
		return outreach.SequenceSignature{}, repo.activeSignatureErr
	}
	if repo.activeSignature != nil {
		return *repo.activeSignature, nil
	}
	return outreach.DefaultSequenceSignature(), nil
}

func (repo *mockRepo) GetGreetingFacts(_ context.Context, restaurantID uuid.UUID) (outreach.GreetingFacts, error) {
	if repo.greetingErr != nil {
		return outreach.GreetingFacts{}, repo.greetingErr
	}
	facts, ok := repo.greetingFacts[restaurantID]
	if !ok {
		return outreach.GreetingFacts{}, repository.ErrNotFound
	}
	return facts, nil
}

func (repo *mockRepo) GetSequenceDelivery(context.Context, uuid.UUID, int) (outreach.SequenceDelivery, error) {
	return repo.delivery, nil
}

func (repo *mockRepo) PrepareSequenceDelivery(_ context.Context, _ uuid.UUID, step int, subject, bodyHTML, bodyText string) error {
	repo.prepared = outreach.RenderedSequenceStep{Position: step, Subject: subject, BodyText: bodyText}
	repo.preparedHTML = bodyHTML
	return nil
}

func (repo *mockRepo) FinalizeSequenceDelivery(_ context.Context, finalization outreach.SequenceDeliveryFinalization) error {
	repo.finalizations = append(repo.finalizations, finalization)
	return nil
}

func (repo *mockRepo) NextSequenceDueAt(context.Context) (*time.Time, error) {
	return repo.nextDue, nil
}

type mockEmailProvider struct {
	request emailprovider.SendRequest
	result  emailprovider.SendResult
	err     error
	sends   int
}

func (provider *mockEmailProvider) Send(_ context.Context, req emailprovider.SendRequest) (emailprovider.SendResult, error) {
	provider.sends++
	provider.request = req
	return provider.result, provider.err
}

type fallbackRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip fallbackRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func testAccountPool(t *testing.T, provider emailprovider.Provider) *emailprovider.AccountPool {
	t.Helper()
	pool, err := emailprovider.NewAccountPool([]emailprovider.Provider{provider}, 50, 150)
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	return pool
}

func newSequenceService(t *testing.T, repo *mockRepo, provider *mockEmailProvider) *outreach.Service {
	t.Helper()
	campaignRepo := &campaigns.Mock{}
	campaignService := campaigns.NewService(campaignRepo, nil, nil, nil, config.AppURLsConfig{
		PublicBaseURL: "https://api.example.com",
	})
	return outreach.NewService(
		repo,
		nil,
		campaignRepo,
		campaignService,
		nil,
		outreach.DemoTokenResolver{},
		testAccountPool(t, provider),
		nil,
		config.EmailConfig{Provider: "fake"},
		config.OutreachConfig{BulkMax: 150},
		config.AppURLsConfig{
			PresentationSiteURL: "https://tuvisolutions.com/services/restaurants",
			PublicMarketingURL:  "https://tuvisolutions.com",
		},
		nil,
		nil,
	)
}

func internalAdminPrincipal() auth.Principal {
	return auth.Principal{UserID: uuid.New(), Role: auth.RoleInternalAdmin}
}

func TestGetStatusIncludesConfirmedSentCountsByPhase(t *testing.T) {
	want := outreach.SentCounts{Total: 14, Phase1: 8, Phase2: 4, Phase3: 1, Other: 1}
	repo := &statusCountsRepo{
		mockRepo:   &mockRepo{count: 5},
		sentCounts: want,
	}
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
		config.OutreachConfig{BulkMax: 150},
		config.AppURLsConfig{},
		nil,
		nil,
	)

	result, err := service.GetStatus(context.Background(), internalAdminPrincipal())
	if err != nil {
		t.Fatalf("GetStatus() error = %v", err)
	}
	if result.SentCounts != want {
		t.Fatalf("GetStatus().SentCounts = %#v, want %#v", result.SentCounts, want)
	}
	if result.PendingEligibleCount != 5 {
		t.Fatalf("GetStatus().PendingEligibleCount = %d, want 5", result.PendingEligibleCount)
	}
}

func TestGetStatusReturnsSentCountError(t *testing.T) {
	wantErr := errors.New("sent count unavailable")
	repo := &statusCountsRepo{
		mockRepo: &mockRepo{},
		sentErr:  wantErr,
	}
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

	_, err := service.GetStatus(context.Background(), internalAdminPrincipal())
	if !errors.Is(err, wantErr) {
		t.Fatalf("GetStatus() error = %v, want %v", err, wantErr)
	}
}

func eligibleSequenceRepo() *mockRepo {
	restaurantID := uuid.New()
	campaignID := uuid.New()
	consentAt := time.Now().UTC()
	step := outreach.SequenceStep{
		ID: uuid.New(), SequenceID: uuid.New(), Position: 1, Enabled: true,
		SubjectTemplate:  "A practical idea for {{restaurant_name}}",
		BodyTextTemplate: "{{greeting}}\n\nI had one practical idea for {{restaurant_name}}. Open to a quick note back?",
	}
	return &mockRepo{
		leads:       []outreach.EligibleLead{{CampaignID: campaignID, RestaurantID: restaurantID, Step: 1}},
		activeSteps: []outreach.SequenceStep{step},
		delivery: outreach.SequenceDelivery{
			CampaignID: campaignID, RestaurantID: restaurantID,
			RestaurantName: "Test Cafe", RecipientEmail: "owner@example.com",
			LifecycleStatus: restaurants.StatusLead,
			ConsentBasis:    "inferred_business", ConsentSource: "business_contact_import",
			ConsentRecordedAt: &consentAt, ConsentEvidence: []byte(`{"policy":"inferred_business"}`),
			SequenceStatus: outreach.SequenceStatusApproved, Step: step,
			Signature:     outreach.DefaultSequenceSignature(),
			GreetingFacts: outreach.GreetingFacts{RestaurantName: "Test Cafe"},
		},
	}
}

func TestGreeting01IsIdenticalAcrossLivePreviewAndTemplateTest(t *testing.T) {
	restaurantID := uuid.New()
	sequenceID := uuid.New()
	campaignID := uuid.New()
	consentAt := time.Now().UTC()
	rating := 4.7
	reviewCount := 380
	facts := outreach.GreetingFacts{
		RestaurantName: "Spice Garden", OwnerFirstName: "Maya",
		GooglePlaceID: "place-1", ScrapeStatus: "success", City: "Plano",
		Cuisines: []byte(`["Indian Restaurant"]`),
		Rating:   &rating, ReviewCount: &reviewCount,
	}
	step := outreach.SequenceStep{
		ID: uuid.New(), SequenceID: sequenceID, Position: 1, Enabled: true,
		SubjectTemplate:  "A practical idea for {{restaurant_name}}",
		BodyTextTemplate: "[GREETING]\n\nThe online flow could make it easier for guests.",
	}
	newRepo := func() *mockRepo {
		return &mockRepo{
			leads:       []outreach.EligibleLead{{CampaignID: campaignID, RestaurantID: restaurantID, Step: 1}},
			activeSteps: []outreach.SequenceStep{step}, sequenceSteps: []outreach.SequenceStep{step},
			greetingFacts: map[uuid.UUID]outreach.GreetingFacts{restaurantID: facts},
			delivery: outreach.SequenceDelivery{
				CampaignID: campaignID, RestaurantID: restaurantID,
				RestaurantName: facts.RestaurantName, OwnerFirstName: facts.OwnerFirstName,
				RecipientEmail: "owner@example.com", LifecycleStatus: restaurants.StatusLead,
				ConsentBasis: "inferred_business", ConsentSource: "business_contact_import",
				ConsentRecordedAt: &consentAt, ConsentEvidence: []byte(`{"policy":"inferred_business"}`),
				SequenceStatus: outreach.SequenceStatusApproved, Step: step, GreetingFacts: facts,
			},
		}
	}

	liveProvider := &mockEmailProvider{result: emailprovider.SendResult{ProviderMessageID: "live"}}
	liveService := newSequenceService(t, newRepo(), liveProvider)
	if _, err := liveService.RunBulkSend(context.Background(), uuid.New()); err != nil {
		t.Fatalf("RunBulkSend() error = %v", err)
	}

	testProvider := &mockEmailProvider{result: emailprovider.SendResult{ProviderMessageID: "test"}}
	testService := newSequenceService(t, newRepo(), testProvider)
	result, err := testService.SendTemplateTest(context.Background(), internalAdminPrincipal(), outreach.TemplateTestSendInput{
		RecipientEmail: "test@example.com", RestaurantID: &restaurantID,
		RestaurantName: "Ignored Synthetic Name", OwnerFirstName: "Ignored",
	})
	if err != nil {
		t.Fatalf("SendTemplateTest() error = %v", err)
	}
	preview, err := testService.PreviewSequence(context.Background(), internalAdminPrincipal(), sequenceID, outreach.PreviewSequenceInput{
		RestaurantID: &restaurantID, RestaurantName: "Ignored Synthetic Name", OwnerFirstName: "Ignored",
	})
	if err != nil {
		t.Fatalf("PreviewSequence() error = %v", err)
	}

	if liveProvider.request.TextBody != testProvider.request.TextBody {
		t.Fatalf("live and test bodies differ:\nlive=%q\ntest=%q", liveProvider.request.TextBody, testProvider.request.TextBody)
	}
	if len(preview.Steps) != 1 || !strings.HasPrefix(liveProvider.request.TextBody, preview.Steps[0].BodyText+"\n\n") {
		t.Fatalf("preview body is not the exact unsigned prefix of delivery: preview=%#v live=%q", preview.Steps, liveProvider.request.TextBody)
	}
	if preview.Greeting01 != result.Greeting01 || strings.Join(preview.FactsUsed, ",") != strings.Join(result.FactsUsed, ",") {
		t.Fatalf("preview/test greeting audit differs: preview=%#v test=%#v", preview, result)
	}
	if preview.RestaurantName != "Spice Garden" || result.RestaurantName != "Spice Garden" || strings.Contains(preview.Greeting01, "Ignored") {
		t.Fatalf("restaurant_id did not override synthetic inputs: preview=%#v test=%#v", preview, result)
	}
	wantGreeting01 := outreach.RenderGreeting01(facts).Greeting01
	if result.Greeting01 != wantGreeting01 || preview.Greeting01 != wantGreeting01 {
		t.Fatalf("greeting01 audit differs from the Template 1 renderer: want=%q preview=%q test=%q", wantGreeting01, preview.Greeting01, result.Greeting01)
	}
	for path, body := range map[string]string{
		"live":    liveProvider.request.TextBody,
		"preview": preview.Steps[0].BodyText,
		"test":    testProvider.request.TextBody,
	} {
		if count := strings.Count(body, wantGreeting01); count != 1 {
			t.Errorf("%s Template 1 greeting01 count = %d, want 1: %q", path, count, body)
		}
		if strings.Contains(body, "Hi Maya,") {
			t.Errorf("%s Template 1 contains legacy greeting: %q", path, body)
		}
	}
}

func TestGreetingRestaurantLookupAuthorizationMissingAndSyntheticCompatibility(t *testing.T) {
	sequenceID := uuid.New()
	restaurantID := uuid.New()
	step := outreach.SequenceStep{
		ID: uuid.New(), SequenceID: sequenceID, Position: 1, Enabled: true,
		SubjectTemplate:  "Hello {{restaurant_name}}",
		BodyTextTemplate: "{{greeting01}}\n\nA short note.",
	}
	repo := &mockRepo{activeSteps: []outreach.SequenceStep{step}, sequenceSteps: []outreach.SequenceStep{step}}
	service := newSequenceService(t, repo, &mockEmailProvider{})
	owner := auth.Principal{UserID: uuid.New(), Role: auth.RoleRestaurantOwner}
	if _, err := service.PreviewSequence(context.Background(), owner, sequenceID, outreach.PreviewSequenceInput{RestaurantID: &restaurantID}); !errors.Is(err, restaurants.ErrForbidden) {
		t.Fatalf("PreviewSequence() error = %v, want forbidden", err)
	}
	if _, err := service.SendTemplateTest(context.Background(), owner, outreach.TemplateTestSendInput{
		RecipientEmail: "test@example.com", RestaurantID: &restaurantID,
	}); !errors.Is(err, restaurants.ErrForbidden) {
		t.Fatalf("SendTemplateTest() error = %v, want forbidden", err)
	}

	if _, err := service.PreviewSequence(context.Background(), internalAdminPrincipal(), sequenceID, outreach.PreviewSequenceInput{RestaurantID: &restaurantID}); !errors.Is(err, outreach.ErrGreetingRestaurantNotFound) {
		t.Fatalf("PreviewSequence() error = %v, want missing restaurant", err)
	}
	if _, err := service.SendTemplateTest(context.Background(), internalAdminPrincipal(), outreach.TemplateTestSendInput{
		RecipientEmail: "test@example.com", RestaurantID: &restaurantID,
	}); !errors.Is(err, outreach.ErrGreetingRestaurantNotFound) {
		t.Fatalf("SendTemplateTest() error = %v, want missing restaurant", err)
	}

	preview, err := service.PreviewSequence(context.Background(), internalAdminPrincipal(), sequenceID, outreach.PreviewSequenceInput{
		RestaurantName: "Synthetic Cafe", OwnerFirstName: "Sam",
	})
	if err != nil {
		t.Fatalf("synthetic PreviewSequence() error = %v", err)
	}
	if preview.RestaurantID != nil || !strings.HasPrefix(preview.Greeting01, "Morning Sam,\n\nI noticed Synthetic Cafe has been building a local following.") {
		t.Fatalf("synthetic preview = %#v, want backward-compatible name/owner rendering", preview)
	}
}

func TestRunBulkSendFinalizesAcceptedSequenceWithSharedSignature(t *testing.T) {
	repo := eligibleSequenceRepo()
	provider := &mockEmailProvider{result: emailprovider.SendResult{ProviderMessageID: "mock"}}
	service := newSequenceService(t, repo, provider)

	summary, err := service.RunBulkSend(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("RunBulkSend() error = %v", err)
	}
	if summary.Sent != 1 || summary.Attempted != 1 {
		t.Fatalf("summary = %#v, want one sent attempt", summary)
	}
	if !strings.Contains(provider.request.HTMLBody, "tuvi-solutions-logo-transparent.png") ||
		!strings.Contains(provider.request.HTMLBody, "Praveen Maurya") {
		t.Fatalf("HTMLBody missing shared logo signature: %q", provider.request.HTMLBody)
	}
	if got := len(strings.FieldsFunc(provider.request.TextBody, func(r rune) bool { return r == '\n' })); got == 0 {
		t.Fatal("TextBody is empty")
	}
	for _, token := range []string{"Thanks & Regards,", "Praveen Maurya", "Business Development Manager", "Tuvi Solutions", "https://tuvisolutions.com"} {
		if !strings.Contains(provider.request.TextBody, token) {
			t.Fatalf("TextBody missing signature token %q", token)
		}
	}
	if repo.prepared.BodyText != provider.request.TextBody {
		t.Fatalf("prepared BodyText does not match sent TextBody")
	}
	if repo.preparedHTML != provider.request.HTMLBody {
		t.Fatalf("prepared HTMLBody does not match sent HTMLBody")
	}
	if len(repo.finalizations) != 1 || repo.finalizations[0].Outcome != "sent" || repo.finalizations[0].Step != 1 {
		t.Fatalf("finalizations = %#v, want confirmed step 1", repo.finalizations)
	}
}

func TestRunBulkSendUsesActiveSignatureForPinnedCampaign(t *testing.T) {
	repo := eligibleSequenceRepo()
	repo.delivery.Signature = outreach.SequenceSignature{
		Name: "Archived Sender", Title: "Old title",
	}
	activeSignature := outreach.SequenceSignature{
		Name: "Alex Morgan", Title: "Partnerships Manager",
		AdditionalDetails: "Phone: +61 400 000 000\nAddress: 10 Current Street",
	}
	repo.activeSignature = &activeSignature
	provider := &mockEmailProvider{result: emailprovider.SendResult{ProviderMessageID: "mock"}}
	service := newSequenceService(t, repo, provider)

	if _, err := service.RunBulkSend(context.Background(), uuid.New()); err != nil {
		t.Fatalf("RunBulkSend() error = %v", err)
	}
	for _, token := range []string{"Alex Morgan", "Partnerships Manager", "Phone: +61 400 000 000", "Address: 10 Current Street"} {
		if !strings.Contains(provider.request.TextBody, token) || !strings.Contains(provider.request.HTMLBody, token) {
			t.Fatalf("active sequence signature missing %q: %#v", token, provider.request)
		}
	}
	if strings.Contains(provider.request.TextBody, "Archived Sender") || strings.Contains(provider.request.HTMLBody, "Archived Sender") {
		t.Fatalf("pinned archived signature leaked into request: %#v", provider.request)
	}
	if repo.prepared.BodyText != provider.request.TextBody || repo.preparedHTML != provider.request.HTMLBody {
		t.Fatalf("prepared artifact does not match active-signed provider request")
	}
}

func TestRunBulkSendFailsClosedWhenActiveSignatureIsUnavailable(t *testing.T) {
	repo := eligibleSequenceRepo()
	sentinel := errors.New("active signature unavailable")
	repo.activeSignatureErr = sentinel
	provider := &mockEmailProvider{result: emailprovider.SendResult{ProviderMessageID: "must-not-send"}}
	service := newSequenceService(t, repo, provider)

	summary, err := service.RunBulkSend(context.Background(), uuid.New())
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunBulkSend() error = %v, want active signature error", err)
	}
	if provider.sends != 0 {
		t.Fatalf("provider sends = %d, want zero", provider.sends)
	}
	if repo.prepared != (outreach.RenderedSequenceStep{}) || repo.preparedHTML != "" {
		t.Fatalf("delivery was prepared before signature resolution: %#v / %q", repo.prepared, repo.preparedHTML)
	}
	if len(repo.finalizations) != 0 {
		t.Fatalf("finalizations = %#v, want none", repo.finalizations)
	}
	if summary.Attempted != 1 || summary.Failed != 1 || summary.StoppedReason != "delivery_error" {
		t.Fatalf("summary = %#v, want one failed delivery_error", summary)
	}
}

func TestRunBulkSendDoesNotScheduleNonDurableRateLimitFailure(t *testing.T) {
	repo := eligibleSequenceRepo()
	provider := &mockEmailProvider{err: fmt.Errorf("%w: simulated local provider rejection", emailprovider.ErrRetryableRejection)}
	service := newSequenceService(t, repo, provider)

	summary, err := service.RunBulkSend(context.Background(), uuid.New())
	if !errors.Is(err, emailprovider.ErrRetryableRejection) {
		t.Fatalf("RunBulkSend() error = %v, want ErrRetryableRejection", err)
	}
	if summary.Attempted != 1 || summary.Failed != 1 || summary.StoppedReason != "delivery_error" {
		t.Fatalf("summary = %#v, want one failed delivery_error", summary)
	}
	if len(repo.finalizations) != 1 || repo.finalizations[0].Outcome != "unknown" {
		t.Fatalf("finalizations = %#v, want one unknown outcome", repo.finalizations)
	}
}

func TestRunBulkSendSkipDoesNotAdvanceSequence(t *testing.T) {
	repo := eligibleSequenceRepo()
	provider := &mockEmailProvider{result: emailprovider.SendResult{Skipped: true}}
	service := newSequenceService(t, repo, provider)

	summary, err := service.RunBulkSend(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("RunBulkSend() error = %v", err)
	}
	if summary.Skipped != 1 || len(repo.finalizations) != 1 || repo.finalizations[0].Outcome != "skipped" {
		t.Fatalf("summary/finalizations = %#v / %#v", summary, repo.finalizations)
	}
}

func TestRunBulkSendProviderFailureRetainsSequenceAsUnknown(t *testing.T) {
	repo := eligibleSequenceRepo()
	provider := &mockEmailProvider{err: errors.New("provider outcome unknown")}
	service := newSequenceService(t, repo, provider)

	_, err := service.RunBulkSend(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("RunBulkSend() error = nil, want provider failure")
	}
	if len(repo.finalizations) != 1 || repo.finalizations[0].Outcome != "unknown" || repo.finalizations[0].Step != 1 {
		t.Fatalf("finalizations = %#v, want unknown without advancement", repo.finalizations)
	}
}

func TestSendTemplateTestEmailsSendsSavedSequenceExactlyWithSignature(t *testing.T) {
	repo := eligibleSequenceRepo()
	provider := &mockEmailProvider{result: emailprovider.SendResult{ProviderMessageID: "message-1"}}
	service := newSequenceService(t, repo, provider)

	result, err := service.SendTemplateTest(context.Background(), internalAdminPrincipal(), outreach.TemplateTestSendInput{
		RecipientEmail: "test@example.com",
		RestaurantName: "Signature Cafe",
		OwnerFirstName: "Casey",
	})
	if err != nil {
		t.Fatalf("SendTemplateTest() error = %v", err)
	}
	if result.RecipientEmail != "test@example.com" || len(result.Items) != 1 {
		t.Fatalf("result = %#v, want the one enabled sequence item", result)
	}
	if provider.request.To != "test@example.com" {
		t.Fatalf("last request To = %q", provider.request.To)
	}
	if provider.request.Subject != "A practical idea for Signature Cafe" {
		t.Fatalf("Subject = %q, want the rendered saved subject without a test prefix", provider.request.Subject)
	}
	if !strings.Contains(provider.request.HTMLBody, "tuvi-solutions-logo-transparent.png") ||
		!strings.Contains(provider.request.HTMLBody, "Praveen Maurya") {
		t.Fatalf("HTMLBody missing shared logo signature: %q", provider.request.HTMLBody)
	}
	if !strings.Contains(provider.request.TextBody, "Praveen Maurya") ||
		!strings.Contains(provider.request.TextBody, "https://tuvisolutions.com") {
		t.Fatalf("last request missing text signature: %q", provider.request.TextBody)
	}
}

func TestSendTemplateTestBypassesManualAccountPoolLimit(t *testing.T) {
	t.Parallel()

	repo := eligibleSequenceRepo()
	provider := &mockEmailProvider{result: emailprovider.SendResult{ProviderMessageID: "manual-test"}}
	service := newSequenceService(t, repo, provider)

	for index := range 51 {
		_, err := service.SendTemplateTest(context.Background(), internalAdminPrincipal(), outreach.TemplateTestSendInput{
			RecipientEmail: "test@example.com",
			RestaurantName: "Manual Test Cafe",
		})
		if err != nil {
			t.Fatalf("manual template send %d error = %v", index+1, err)
		}
	}
	if provider.sends != 51 {
		t.Fatalf("provider sends = %d, want 51 manual sends without account-pool exhaustion", provider.sends)
	}
}

func TestSendTemplateTestFallbackBypassesManualAccountLimit(t *testing.T) {
	sequenceID := uuid.New()
	repo := eligibleSequenceRepo()
	repo.activeSteps = []outreach.SequenceStep{
		{
			ID: uuid.New(), SequenceID: sequenceID, Position: 1, Enabled: true,
			SubjectTemplate:  "First note for {{restaurant_name}}",
			BodyTextTemplate: "{{greeting01}}\n\nFirst manual test body.",
		},
		{
			ID: uuid.New(), SequenceID: sequenceID, Position: 2, Enabled: true, DelayHours: 72,
			SubjectTemplate:  "Second note for {{restaurant_name}}",
			BodyTextTemplate: "{{greeting}}\n\nSecond manual test body.",
		},
	}

	originalTransport := http.DefaultTransport
	tokenRequests := 0
	sendRequests := 0
	http.DefaultTransport = fallbackRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		switch {
		case request.Method == http.MethodPost && request.URL.Host == "oauth2.googleapis.com" && request.URL.Path == "/token":
			tokenRequests++
			body = `{"access_token":"isolated-test-token","expires_in":3600}`
		case request.Method == http.MethodPost && request.URL.Host == "gmail.googleapis.com" && request.URL.Path == "/gmail/v1/users/me/messages/send":
			sendRequests++
			body = fmt.Sprintf(`{"id":"manual-%d","threadId":"thread-%d"}`, sendRequests, sendRequests)
		default:
			return nil, fmt.Errorf("unexpected fallback email request: %s %s", request.Method, request.URL.Redacted())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	campaignRepo := &campaigns.Mock{}
	service := outreach.NewService(
		repo,
		nil,
		campaignRepo,
		campaigns.NewService(campaignRepo, nil, nil, nil, config.AppURLsConfig{PublicBaseURL: "https://api.example.com"}),
		nil,
		outreach.DemoTokenResolver{},
		nil,
		nil,
		config.EmailConfig{Provider: "gmail", FromName: "Tuvi"},
		config.OutreachConfig{
			BulkMax:          1,
			EmailsPerAccount: 1,
			GoogleWorkspaceAccounts: []config.GmailMailConfig{{
				AccountKey:   "fallback",
				MailboxEmail: "fallback@example.com",
				FromEmail:    "fallback@example.com",
				ClientID:     "isolated-client",
				ClientSecret: "isolated-secret",
				RefreshToken: "isolated-refresh",
			}},
		},
		config.AppURLsConfig{
			PresentationSiteURL: "https://tuvisolutions.com/services/restaurants",
			PublicMarketingURL:  "https://tuvisolutions.com",
		},
		nil,
		nil,
	)

	result, err := service.SendTemplateTest(context.Background(), internalAdminPrincipal(), outreach.TemplateTestSendInput{
		RecipientEmail: "test@example.com",
		RestaurantName: "Fallback Cafe",
	})
	if err != nil {
		t.Fatalf("fallback SendTemplateTest() error = %v", err)
	}
	if len(result.Items) != 2 || sendRequests != 2 {
		t.Fatalf("fallback manual sends = %d items/%d requests, want 2 despite account limit 1", len(result.Items), sendRequests)
	}
	if tokenRequests != 1 {
		t.Fatalf("fallback token requests = %d, want 1 isolated provider session", tokenRequests)
	}
}

func TestSendTemplateTestTargetsSelectedSavedDraftAndSignature(t *testing.T) {
	activeSequenceID := uuid.New()
	draftSequenceID := uuid.New()
	activeStep := outreach.SequenceStep{
		ID: uuid.New(), SequenceID: activeSequenceID, Position: 1, Enabled: true,
		SubjectTemplate: "Active subject", BodyTextTemplate: "{{greeting}}\n\nActive body",
	}
	draftStep := outreach.SequenceStep{
		ID: uuid.New(), SequenceID: draftSequenceID, Position: 1, Enabled: true,
		SubjectTemplate: "Draft for [RESTAURANT_NAME]", BodyTextTemplate: "[GREETING]\n\nDraft body",
	}
	repo := &mockRepo{
		activeSteps:   []outreach.SequenceStep{activeStep},
		sequenceSteps: []outreach.SequenceStep{draftStep},
		signatures: map[uuid.UUID]outreach.SequenceSignature{
			draftSequenceID: {
				Name: "Alex Morgan", Title: "Partnerships Manager",
				AdditionalDetails: "Phone: +61 400 000 000",
			},
		},
	}
	activeSignature := outreach.SequenceSignature{
		Name: "Active Sender", Title: "Active title",
		AdditionalDetails: "Phone: active",
	}
	repo.activeSignature = &activeSignature
	provider := &mockEmailProvider{result: emailprovider.SendResult{ProviderMessageID: "draft-test"}}
	service := newSequenceService(t, repo, provider)

	result, err := service.SendTemplateTest(context.Background(), internalAdminPrincipal(), outreach.TemplateTestSendInput{
		RecipientEmail: "test@example.com", SequenceID: &draftSequenceID,
		RestaurantName: "Signature Cafe", OwnerFirstName: "Casey",
	})
	if err != nil {
		t.Fatalf("SendTemplateTest() error = %v", err)
	}
	if result.SequenceID != draftSequenceID || provider.request.Subject != "Draft for Signature Cafe" || strings.Contains(provider.request.TextBody, "Active body") {
		t.Fatalf("selected draft was not rendered exactly: result=%#v request=%#v", result, provider.request)
	}
	for _, token := range []string{"Alex Morgan", "Partnerships Manager", "Phone: +61 400 000 000"} {
		if !strings.Contains(provider.request.TextBody, token) || !strings.Contains(provider.request.HTMLBody, token) {
			t.Fatalf("selected signature missing %q: %#v", token, provider.request)
		}
	}
	if strings.Contains(provider.request.TextBody, "Active Sender") || strings.Contains(provider.request.HTMLBody, "Active Sender") {
		t.Fatalf("active signature replaced selected draft signature: %#v", provider.request)
	}
}

type scheduledAccountUnavailablePool struct {
	next  time.Time
	sends int
	err   error
}

func (pool *scheduledAccountUnavailablePool) Send(context.Context, emailprovider.SendRequest) (emailprovider.SendResult, error) {
	pool.sends++
	if pool.err != nil {
		return emailprovider.SendResult{QuotaManaged: true, Finalized: true, AccountKey: "limited"}, pool.err
	}
	return emailprovider.SendResult{QuotaManaged: true, Finalized: true, AccountKey: "revoked"},
		fmt.Errorf("%w: revoked credential", emailprovider.ErrAccountUnavailable)
}

func (*scheduledAccountUnavailablePool) Configured(context.Context) (bool, error) { return true, nil }
func (*scheduledAccountUnavailablePool) Durable() bool                            { return true }
func (pool *scheduledAccountUnavailablePool) NextAvailableAt(context.Context) (*time.Time, error) {
	next := pool.next
	return &next, nil
}
func (*scheduledAccountUnavailablePool) Exhausted() bool { return false }
func (pool *scheduledAccountUnavailablePool) AcquireDirect(context.Context) (emailprovider.Provider, error) {
	return pool, nil
}
func (pool *scheduledAccountUnavailablePool) SendDirect(ctx context.Context, request emailprovider.SendRequest) (emailprovider.SendResult, error) {
	return pool.Send(ctx, request)
}
func (pool *scheduledAccountUnavailablePool) SendDirectFrom(ctx context.Context, _ string, request emailprovider.SendRequest) (emailprovider.SendResult, error) {
	return pool.Send(ctx, request)
}

func newSequenceServiceWithEmailPool(repo *mockRepo, pool emailprovider.AccountPoolProvider) *outreach.Service {
	campaignRepo := &campaigns.Mock{}
	return outreach.NewService(
		repo,
		nil,
		campaignRepo,
		campaigns.NewService(campaignRepo, nil, nil, nil, config.AppURLsConfig{PublicBaseURL: "https://api.example.com"}),
		nil,
		outreach.DemoTokenResolver{},
		pool,
		nil,
		config.EmailConfig{Provider: "fake"},
		config.OutreachConfig{BulkMax: 150},
		config.AppURLsConfig{
			PresentationSiteURL: "https://tuvisolutions.com/services/restaurants",
			PublicMarketingURL:  "https://tuvisolutions.com",
		},
		nil,
		nil,
	)
}

func TestRunBulkSendDefersAfterScheduledAccountAuthRejection(t *testing.T) {
	repo := eligibleSequenceRepo()
	next := time.Now().UTC().Add(3 * time.Minute)
	pool := &scheduledAccountUnavailablePool{next: next}
	service := newSequenceServiceWithEmailPool(repo, pool)

	summary, err := service.RunBulkSend(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("RunBulkSend() error = %v", err)
	}
	if pool.sends != 1 || summary.Attempted != 1 || summary.Failed != 1 {
		t.Fatalf("scheduled failure = sends %d, summary %#v", pool.sends, summary)
	}
	if summary.StoppedReason != "account_unavailable" || summary.NextAvailableAt == nil || !summary.NextAvailableAt.Equal(next) {
		t.Fatalf("scheduled deferral = %#v, want account_unavailable at %s", summary, next)
	}
	if len(repo.finalizations) != 0 {
		t.Fatalf("service finalizations = %#v, want durable pool to own finalization", repo.finalizations)
	}
}

func TestRunBulkSendDefersAfterScheduledRateLimitRejection(t *testing.T) {
	repo := eligibleSequenceRepo()
	next := time.Now().UTC().Add(3 * time.Minute)
	pool := &scheduledAccountUnavailablePool{
		next: next,
		err:  fmt.Errorf("%w: gmail rate limit", emailprovider.ErrRetryableRejection),
	}
	service := newSequenceServiceWithEmailPool(repo, pool)

	summary, err := service.RunBulkSend(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("RunBulkSend() error = %v", err)
	}
	if pool.sends != 1 || summary.Attempted != 1 || summary.Failed != 1 {
		t.Fatalf("scheduled failure = sends %d, summary %#v", pool.sends, summary)
	}
	if summary.StoppedReason != "retryable_provider_rejection" || summary.NextAvailableAt == nil || !summary.NextAvailableAt.Equal(next) {
		t.Fatalf("scheduled deferral = %#v, want retryable_provider_rejection at %s", summary, next)
	}
	if len(repo.finalizations) != 0 {
		t.Fatalf("service finalizations = %#v, want durable pool to own finalization", repo.finalizations)
	}
}

func TestSendTemplateTestKeepsUnavailableAccountOutOfMultiStepSnapshot(t *testing.T) {
	sequenceID := uuid.New()
	repo := &mockRepo{activeSteps: []outreach.SequenceStep{
		{
			ID: uuid.New(), SequenceID: sequenceID, Position: 1, Enabled: true,
			SubjectTemplate: "First for {{restaurant_name}}", BodyTextTemplate: "{{greeting}}\n\nFirst body.",
		},
		{
			ID: uuid.New(), SequenceID: sequenceID, Position: 2, Enabled: true, DelayHours: 72,
			SubjectTemplate: "Second for {{restaurant_name}}", BodyTextTemplate: "{{greeting}}\n\nSecond body.",
		},
	}}
	unavailable := &mockEmailProvider{err: fmt.Errorf("%w: revoked refresh token", emailprovider.ErrAccountUnavailable)}
	healthy := &mockEmailProvider{result: emailprovider.SendResult{ProviderMessageID: "accepted"}}
	pool, err := emailprovider.NewAccountPool([]emailprovider.Provider{unavailable, healthy}, 40, 80)
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	service := newSequenceServiceWithEmailPool(repo, pool)

	result, err := service.SendTemplateTest(context.Background(), internalAdminPrincipal(), outreach.TemplateTestSendInput{
		RecipientEmail: "test@example.com", RestaurantName: "Fallback Cafe",
	})
	if err != nil {
		t.Fatalf("SendTemplateTest() error = %v", err)
	}
	if len(result.Items) != 2 || unavailable.sends != 1 || healthy.sends != 2 {
		t.Fatalf("multi-step failover = %d items, bad/healthy sends %d/%d; want 2, 1/2", len(result.Items), unavailable.sends, healthy.sends)
	}
}
