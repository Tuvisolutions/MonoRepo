package outreach

import (
	"strings"
	"testing"
)

func TestEligibleLeadQueryUsesStrictLifecyclePriorityOrderingAndSharedEmailLimit(t *testing.T) {
	if strings.Contains(eligibleLeadsBaseQuery, "demo_ready") {
		t.Fatal("eligible query still accepts demo_ready")
	}
	if strings.Count(eligibleLeadsBaseQuery, "status IN ('lead', 'emailed')") != 1 {
		t.Fatal("eligible query must evaluate lifecycle only for the selected recipient")
	}
	for _, forbidden := range []string{"existing_campaign", "NOT EXISTS"} {
		if strings.Contains(eligibleLeadsBaseQuery, forbidden) {
			t.Fatalf("eligible query still lets another campaign block this recipient through %q", forbidden)
		}
	}
	if !strings.Contains(eligibleLeadsOrderBy, "ORDER BY (campaign.current_step > 0) DESC") {
		t.Fatal("due follow-ups must retain ordering priority")
	}
	for _, required := range []string{
		"outreach_consent_basis = 'inferred_business'",
		"outreach_consent_evidence <> '{}'::jsonb",
		"shown_interest = false",
	} {
		if !strings.Contains(eligibleLeadsBaseQuery, required) {
			t.Fatalf("eligible query missing policy guard %q", required)
		}
	}
	if strings.Count(eligibleLeadsBaseQuery, ") <= 3") != 1 {
		t.Fatal("eligible query must apply the shared-email limit only to the selected recipient")
	}
}

func TestRecipientStatusCountsDoNotHideNewRecipientsBehindFollowups(t *testing.T) {
	for _, forbidden := range []string{"due_followups", "NOT (SELECT"} {
		if strings.Contains(recipientStatusCountsQuery, forbidden) {
			t.Fatalf("recipient counts still hide new recipients behind follow-ups through %q", forbidden)
		}
	}
	if !strings.Contains(recipientStatusCountsQuery, "current_step = 0 AND next_send_at <= now()") {
		t.Fatal("recipient counts must include every due, policy-eligible new recipient")
	}
}

func TestSentDeliveryCountsQueryUsesOneConfirmedAttemptAggregate(t *testing.T) {
	if strings.Count(strings.ToUpper(sentDeliveryCountsQuery), "SELECT") != 1 {
		t.Fatalf("sent delivery counts must use one aggregate query: %q", sentDeliveryCountsQuery)
	}
	for _, required := range []string{
		"FROM email_delivery_attempts",
		"WHERE status = 'sent'",
		"FILTER (WHERE campaign_step = 1)",
		"FILTER (WHERE campaign_step = 2)",
		"FILTER (WHERE campaign_step = 3)",
		"FILTER (WHERE campaign_step NOT IN (1, 2, 3))",
	} {
		if !strings.Contains(sentDeliveryCountsQuery, required) {
			t.Fatalf("sent delivery counts query missing %q", required)
		}
	}
}

func TestGreetingOwnerPrecedenceRemainsApolloFirstNameThenApolloNameThenOwners(t *testing.T) {
	wantOrder := []string{
		"apollo_lead #>> '{contact,first_name}'",
		"apollo_lead #>> '{contact,name}'",
		"profile.owners ->> 0",
	}
	last := -1
	for _, fragment := range wantOrder {
		position := strings.Index(ownerFirstNameSelectExpression, fragment)
		if position <= last {
			t.Fatalf("owner precedence expression %q does not preserve order %#v", ownerFirstNameSelectExpression, wantOrder)
		}
		last = position
	}
}

func TestSharedEmailGroupsQueryListsOnlyRepeatedValidEmails(t *testing.T) {
	for _, required := range []string{
		"GROUP BY lower(trim(email))",
		"HAVING count(*) > 1",
		"ORDER BY restaurant_count DESC",
	} {
		if !strings.Contains(sharedEmailGroupsQuery, required) {
			t.Fatalf("shared email groups query missing %q", required)
		}
	}
}

func TestActiveSequenceSignatureQueryRequiresCurrentApprovedVersion(t *testing.T) {
	for _, required := range []string{
		"is_active = true",
		"status = 'approved'",
		"approved_at IS NOT NULL",
		"signature_name",
		"signature_title",
		"signature_details",
	} {
		if !strings.Contains(activeSequenceSignatureQuery, required) {
			t.Fatalf("active signature query missing %q", required)
		}
	}
}
