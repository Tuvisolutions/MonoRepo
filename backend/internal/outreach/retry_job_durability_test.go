package outreach

import (
	"strings"
	"testing"
)

func TestRetryableFailureMakesExactRunningBulkJobReclaimableWithoutBreakingLease(t *testing.T) {
	t.Parallel()

	for _, required := range []string{
		"bulk_job_id",
		"status = 'sending'",
	} {
		if !strings.Contains(finalizeEmailDeliveryAttemptQuery, required) {
			t.Fatalf("delivery attempt finalization does not return or guard %q", required)
		}
	}

	for _, required := range []string{
		"attempts = 0",
		"id = $1",
		"job_type = $2",
		"status = 'running'",
	} {
		if !strings.Contains(resetRetryableEmailBulkJobAttemptsQuery, required) {
			t.Fatalf("retryable job recovery query missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"SET status",
		"available_at =",
		"locked_at =",
		"locked_by =",
		"lease_expires_at =",
		"payload =",
	} {
		if strings.Contains(resetRetryableEmailBulkJobAttemptsQuery, forbidden) {
			t.Fatalf("retryable job recovery query disrupts the live lease through %q", forbidden)
		}
	}
}

func TestDeferBulkJobPreservesRunningLeaseFence(t *testing.T) {
	t.Parallel()

	for _, required := range []string{
		"status = 'running'",
		"locked_by = $5",
		"job_type = $4",
	} {
		if !strings.Contains(deferRunningBulkJobQuery, required) {
			t.Fatalf("running deferral query missing lease fence %q", required)
		}
	}
}
