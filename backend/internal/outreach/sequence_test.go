package outreach

import (
	"errors"
	"strings"
	"testing"
)

func validTestStep() SequenceStep {
	return SequenceStep{
		Position: 1, Enabled: true, DelayHours: 0,
		SubjectTemplate:  "A practical idea for {{restaurant_name}}",
		BodyTextTemplate: "{{greeting}}\n\nI had one practical idea for {{restaurant_name}}. Open to a quick note back?",
	}
}

func TestValidateSequenceStepsRequiresImmediateFirstEnabledStep(t *testing.T) {
	step := validTestStep()
	step.DelayHours = 72
	if err := validateSequenceSteps([]SequenceStep{step}); !errors.Is(err, ErrSequenceInvalid) {
		t.Fatalf("validateSequenceSteps() error = %v, want ErrSequenceInvalid", err)
	}
}

func TestValidateAndRenderPlainText(t *testing.T) {
	step := validTestStep()
	if err := validateSequenceSteps([]SequenceStep{step}); err != nil {
		t.Fatalf("validateSequenceSteps() error = %v", err)
	}
	rendered, err := renderSequenceStep(step, GreetingFacts{RestaurantName: "Harbour Cafe", OwnerFirstName: "Ava"})
	if err != nil {
		t.Fatalf("renderSequenceStep() error = %v", err)
	}
	if !strings.HasPrefix(rendered.BodyText, "Hi Ava,") || strings.Contains(rendered.BodyText, "<") {
		t.Fatalf("body = %q, want owner greeting and plain text", rendered.BodyText)
	}
	if strings.Contains(strings.ToLower(rendered.BodyText), "unsubscribe") {
		t.Fatalf("body = %q, application must not append unsubscribe copy", rendered.BodyText)
	}
}

func TestValidateSequenceTemplateAllowsAdminManagedLinks(t *testing.T) {
	step := validTestStep()
	step.BodyTextTemplate += "\n\nTuvi overview: {{website_url}}\nhttps://example.com"
	if err := validateSequenceTemplate(step, true); err != nil {
		t.Fatalf("validateSequenceTemplate() error = %v", err)
	}
}

func TestRenderSquareBracketPlaceholders(t *testing.T) {
	rating := 4.8
	reviews := 426
	step := validTestStep()
	step.SubjectTemplate = "A digital idea for [RESTAURANT_NAME]"
	step.BodyTextTemplate = "[GREETING]\n\nOwner: [FIRST_NAME]\nCuisine: [CUISINE]\nCity: [CITY]\nRating: [RATING]\nReviews: [TOTAL_REVIEWS]\n[WEBSITE_URL]"
	facts := GreetingFacts{
		RestaurantName: "Spice Garden", OwnerFirstName: "Maya",
		GooglePlaceID: "place-1", ScrapeStatus: "success", City: "Plano",
		Cuisines: []byte(`["Indian Restaurant"]`), Rating: &rating, ReviewCount: &reviews,
	}

	if err := validateSequenceSteps([]SequenceStep{step}); err != nil {
		t.Fatalf("validateSequenceSteps() error = %v", err)
	}
	rendered, err := renderSequenceStep(step, facts)
	if err != nil {
		t.Fatalf("renderSequenceStep() error = %v", err)
	}
	greeting01 := RenderGreeting01(facts).Greeting01
	if count := strings.Count(rendered.BodyText, greeting01); count != 1 {
		t.Fatalf("rendered Template 1 greeting01 count = %d, want 1: %q", count, rendered.BodyText)
	}
	if strings.Contains(rendered.BodyText, "Hi Maya,") {
		t.Fatalf("rendered Template 1 contains legacy greeting: %q", rendered.BodyText)
	}
	for _, expected := range []string{
		"Morning Maya,", "Spice Garden", "Owner: Maya", "Cuisine: Indian",
		"City: Plano", "Rating: 4.8", "Reviews: 426", websiteURL,
	} {
		if !strings.Contains(rendered.Subject+"\n"+rendered.BodyText, expected) {
			t.Fatalf("rendered template missing %q: %#v", expected, rendered)
		}
	}
}

func TestRenderPreservesDatabaseManagedUnsubscribeCopy(t *testing.T) {
	step := validTestStep()
	const databaseCopy = "Unsubscribe: https://email-provider.example/preferences"
	step.BodyTextTemplate += "\n\n" + databaseCopy

	rendered, err := renderSequenceStep(step, GreetingFacts{RestaurantName: "Harbour Cafe", OwnerFirstName: "Ava"})
	if err != nil {
		t.Fatalf("renderSequenceStep() error = %v", err)
	}
	if !strings.Contains(rendered.BodyText, databaseCopy) {
		t.Fatalf("body = %q, want database-managed copy unchanged", rendered.BodyText)
	}
}

func TestRenderFallsBackToRestaurantGreeting(t *testing.T) {
	rendered, err := renderSequenceStep(
		validTestStep(), GreetingFacts{RestaurantName: "Harbour Cafe"},
	)
	if err != nil {
		t.Fatalf("renderSequenceStep() error = %v", err)
	}
	if !strings.HasPrefix(rendered.BodyText, "Hi Harbour Cafe team,") {
		t.Fatalf("body = %q, want restaurant fallback greeting", rendered.BodyText)
	}
}

func TestValidateSequenceGreeting01Rules(t *testing.T) {
	first := validTestStep()
	first.BodyTextTemplate = "{{greeting01}}\n\nA non-repeating first paragraph."
	followUp := SequenceStep{
		Position: 2, Enabled: true, DelayHours: 72,
		SubjectTemplate:  "Following up with {{restaurant_name}}",
		BodyTextTemplate: "{{greeting}}\n\nA legacy-compatible follow-up.",
	}
	if err := validateSequenceSteps([]SequenceStep{first, followUp}); err != nil {
		t.Fatalf("validateSequenceSteps() error = %v, want greeting01 first and legacy greeting later", err)
	}

	tests := []struct {
		name  string
		steps []SequenceStep
	}{
		{
			name: "greeting01 in subject",
			steps: []SequenceStep{{
				Position: 1, Enabled: true, SubjectTemplate: "{{greeting01}}", BodyTextTemplate: "Plain text",
			}},
		},
		{
			name: "greeting01 in later email",
			steps: []SequenceStep{validTestStep(), {
				Position: 2, Enabled: true, DelayHours: 72,
				SubjectTemplate: "Follow up", BodyTextTemplate: "{{greeting01}}\n\nLater",
			}},
		},
		{
			name: "greeting01 repeated",
			steps: []SequenceStep{{
				Position: 1, Enabled: true, SubjectTemplate: "Hello",
				BodyTextTemplate: "{{greeting01}}\n{{greeting01}}",
			}},
		},
		{
			name: "greeting01 mixed with legacy greeting",
			steps: []SequenceStep{{
				Position: 1, Enabled: true, SubjectTemplate: "Hello",
				BodyTextTemplate: "{{greeting01}}\n{{greeting}}",
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateSequenceSteps(test.steps); !errors.Is(err, ErrSequenceInvalid) {
				t.Fatalf("validateSequenceSteps() error = %v, want ErrSequenceInvalid", err)
			}
		})
	}
}

func TestValidateAndRenderRejectsUnresolvedOrNonPlainTextTags(t *testing.T) {
	tests := []SequenceStep{
		{Position: 1, Enabled: true, SubjectTemplate: "Hello", BodyTextTemplate: "{{unknown123}}"},
		{Position: 1, Enabled: true, SubjectTemplate: "Hello", BodyTextTemplate: "{{greeting-01}}"},
		{Position: 1, Enabled: true, SubjectTemplate: "{{ greeting01 }}", BodyTextTemplate: "Plain"},
		{Position: 1, Enabled: true, SubjectTemplate: "Hello", BodyTextTemplate: "<strong>not plain</strong>"},
		{Position: 1, Enabled: true, SubjectTemplate: "Hello", BodyTextTemplate: "[UNKNOWN_FIELD]"},
	}
	for _, step := range tests {
		if err := validateSequenceSteps([]SequenceStep{step}); !errors.Is(err, ErrSequenceInvalid) {
			t.Fatalf("validateSequenceSteps(%q) error = %v, want ErrSequenceInvalid", step.BodyTextTemplate, err)
		}
	}

	step := validTestStep()
	step.BodyTextTemplate += "\n\n{{unknown123}}"
	if _, err := renderSequenceStep(step, GreetingFacts{RestaurantName: "Harbour Cafe"}); !errors.Is(err, ErrSequenceInvalid) {
		t.Fatalf("renderSequenceStep() error = %v, want unresolved-tag rejection", err)
	}
}

func TestSequenceApprovalRebasesOnlyUntouchedEnrollments(t *testing.T) {
	for _, required := range []string{
		"campaign.current_step = 0",
		"campaign.last_sent_at IS NULL",
		"FROM email_delivery_attempts",
		"FROM email_events",
		"event.event_type IN ('sent', 'skipped', 'failed')",
	} {
		if !strings.Contains(rebaseUntouchedEnrollmentsQuery, required) {
			t.Fatalf("rebase query missing immutable-history guard %q", required)
		}
	}
}

func TestRecipientProgressSurfacesRetrySafetyHolds(t *testing.T) {
	for _, required := range []string{
		"delivery_failure_not_retryable",
		"delivery_outcome_conflict",
		"delivery_retry_exhausted",
		"delivery_in_progress",
		"campaign_stopped",
		"prior_attempt.status = 'failed'",
		"lower(trim(prior_attempt.error_code)) NOT IN",
		"prior_attempt.status = 'unknown'",
		"prior_attempt.status IN ('sent', 'sending')",
		"prior_attempt.status <> 'skipped'",
	} {
		if !strings.Contains(recipientProgressQuery, required) {
			t.Fatalf("recipient progress query missing retry safety state %q", required)
		}
	}
}
