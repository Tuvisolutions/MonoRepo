package outreach

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type nullRestaurantInboxRow struct{}

func (nullRestaurantInboxRow) Scan(dest ...any) error {
	*(dest[0].(**uuid.UUID)) = nil
	*(dest[1].(**string)) = nil
	*(dest[2].(*string)) = "owner@example.com"
	*(dest[3].(*string)) = "sales-one"
	*(dest[4].(*string)) = "sales@example.com"
	*(dest[5].(*bool)) = true
	*(dest[6].(*int)) = 1
	*(dest[7].(*string)) = "Question about Tuvi"
	*(dest[8].(*string)) = "Hello, I would like to learn more."
	*(dest[9].(*string)) = "owner@example.com"
	*(dest[10].(*string)) = "sales@example.com"
	*(dest[11].(*time.Time)) = time.Date(2026, 8, 14, 1, 1, 0, 0, time.UTC)
	*(dest[12].(*string)) = MessageDirectionOutbound
	*(dest[13].(*string)) = "Thanks for writing"
	*(dest[14].(*time.Time)) = time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	*(dest[15].(*uuid.UUID)) = uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	*(dest[16].(*uuid.UUID)) = uuid.MustParse("11111111-2222-3333-4444-555555555555")
	return nil
}

func TestScanInboxThreadAcceptsUnmatchedMessageWithoutRestaurantName(t *testing.T) {
	thread, err := scanInboxThread(nullRestaurantInboxRow{})
	if err != nil {
		t.Fatalf("scanInboxThread() error = %v", err)
	}
	if thread.RestaurantName != "" || !thread.Unmatched || thread.MailboxKey != "sales-one" || thread.MailboxEmail != "sales@example.com" {
		t.Fatalf("scanInboxThread() = %#v", thread)
	}
	if thread.ReplyMessageID != uuid.MustParse("11111111-2222-3333-4444-555555555555") {
		t.Fatalf("ReplyMessageID = %s", thread.ReplyMessageID)
	}
	if thread.Subject != "Question about Tuvi" || thread.TextSnippet != "Hello, I would like to learn more." {
		t.Fatalf("latest received subject/snippet = %q/%q", thread.Subject, thread.TextSnippet)
	}
	if thread.FromEmail != "owner@example.com" || thread.ToEmail != "sales@example.com" || thread.MailboxEmail != thread.ToEmail {
		t.Fatalf("latest received from/to/mailbox = %q/%q/%q", thread.FromEmail, thread.ToEmail, thread.MailboxEmail)
	}
	if !thread.ReceivedAt.Equal(time.Date(2026, 8, 14, 1, 1, 0, 0, time.UTC)) {
		t.Fatalf("ReceivedAt = %s", thread.ReceivedAt)
	}
	if thread.LastDirection != MessageDirectionOutbound {
		t.Fatalf("LastDirection = %q, want retained outbound activity", thread.LastDirection)
	}
}

func TestInboxThreadsUseLatestReceivedMessageAndOrderByItsTimestamp(t *testing.T) {
	t.Parallel()

	const want = "received_at DESC, reply_message_id DESC"
	if inboxThreadsLatestFirstOrder != want {
		t.Fatalf("inbox thread order = %q, want %q", inboxThreadsLatestFirstOrder, want)
	}
	if !strings.Contains(inboxThreadsListQuery, "ORDER BY "+want) {
		t.Fatalf("inbox list query does not use latest-first order: %s", inboxThreadsListQuery)
	}
	if strings.Contains(inboxThreadsListQuery, "ORDER BY unread_count") {
		t.Fatalf("inbox list query still prioritizes unread count: %s", inboxThreadsListQuery)
	}
	if strings.Contains(inboxThreadsListQuery, "ORDER BY last_at") {
		t.Fatalf("outbound activity can still reorder inbox threads: %s", inboxThreadsListQuery)
	}
	if !strings.Contains(inboxThreadsCTE, "ORDER BY received_at DESC, created_at DESC, id DESC") {
		t.Fatalf("latest inbound selection lacks a deterministic message tie-breaker: %s", inboxThreadsCTE)
	}
	if !strings.Contains(inboxThreadsCTE, "FILTER (WHERE direction = 'inbound'))[1] AS reply_message_id") {
		t.Fatalf("latest inbound message id is not selected explicitly: %s", inboxThreadsCTE)
	}
	if !strings.Contains(inboxThreadsCTE, "JOIN messages inbound ON inbound.id = rollup.reply_message_id") {
		t.Fatalf("latest received fields are not sourced from the reply message snapshot: %s", inboxThreadsCTE)
	}
	for _, field := range []string{
		"inbound.subject",
		"inbound.body_text",
		"inbound.from_email",
		"inbound.to_email",
		"inbound.received_at",
	} {
		if !strings.Contains(inboxThreadsCTE, field) {
			t.Fatalf("latest received snapshot is missing %s: %s", field, inboxThreadsCTE)
		}
	}
}

func TestInboxQuerySupportsGlobalSearch(t *testing.T) {
	t.Parallel()

	for _, field := range []string{
		"restaurant_name",
		"email",
		"subject",
		"text_snippet",
		"from_email",
		"to_email",
		"mailbox_key",
	} {
		if !strings.Contains(inboxThreadsListQuery, field+" ILIKE") {
			t.Fatalf("inbox global search does not include %s: %s", field, inboxThreadsListQuery)
		}
	}
}
