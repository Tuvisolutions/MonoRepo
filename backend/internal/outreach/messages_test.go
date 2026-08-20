package outreach

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	emailprovider "github.com/rajchodisetti/restaurant-platform/backend/internal/providers/email"
)

type activeSignatureReplyRepo struct {
	signature SequenceSignature
	err       error
}

func (*activeSignatureReplyRepo) ListEligibleLeads(context.Context, int) ([]EligibleLead, error) {
	return nil, nil
}

func (*activeSignatureReplyRepo) CountEligibleLeads(context.Context) (int, error) {
	return 0, nil
}

func (repo *activeSignatureReplyRepo) GetActiveSequenceSignature(context.Context) (SequenceSignature, error) {
	return repo.signature, repo.err
}

func TestPrepareInboxReply(t *testing.T) {
	target := Message{
		ID:            uuid.New(),
		Direction:     MessageDirectionInbound,
		FromEmail:     "OWNER@Restaurant.Example",
		ToEmail:       "Sales@One.Example",
		Subject:       "Original subject",
		GmailThreadID: "thread-1",
		RFCMessageID:  "<original@example.com>",
	}
	request, err := prepareInboxReply(target, ReplyMessageInput{BodyText: " Thanks for replying. "})
	if err != nil {
		t.Fatalf("prepareInboxReply() error = %v", err)
	}
	if request.To != "owner@restaurant.example" || request.Subject != "Re: Original subject" {
		t.Fatalf("reply recipient/subject = %q/%q", request.To, request.Subject)
	}
	if request.FromEmail != "sales@one.example" {
		t.Fatalf("reply FromEmail = %q, want receiving address", request.FromEmail)
	}
	if request.TextBody != "Thanks for replying." || request.ThreadID != "thread-1" {
		t.Fatalf("reply body/thread = %q/%q", request.TextBody, request.ThreadID)
	}
	if request.InReplyTo != target.RFCMessageID || request.References != target.RFCMessageID {
		t.Fatalf("reply RFC headers = %q/%q", request.InReplyTo, request.References)
	}
}

func TestInboxReplyUsesActiveSequenceSignature(t *testing.T) {
	target := Message{
		ID:        uuid.New(),
		Direction: MessageDirectionInbound,
		FromEmail: "owner@restaurant.example",
		ToEmail:   "sales@tuvisolutions.com",
		Subject:   "Original subject",
	}
	service := &Service{repo: &activeSignatureReplyRepo{signature: SequenceSignature{
		Name:              "Alex Morgan",
		Title:             "Partnerships Manager",
		AdditionalDetails: "Phone: +61 400 000 000\nAddress: 10 Current Street",
	}}}
	request, err := service.prepareSignedInboxReply(
		context.Background(),
		target,
		ReplyMessageInput{BodyText: "Thanks for replying."},
	)
	if err != nil {
		t.Fatalf("prepareSignedInboxReply() error = %v", err)
	}
	for _, token := range []string{"Alex Morgan", "Phone: +61 400 000 000", "Address: 10 Current Street"} {
		if !strings.Contains(request.TextBody, token) || !strings.Contains(request.HTMLBody, token) {
			t.Fatalf("signed reply missing %q: %#v", token, request)
		}
	}
	if request.Signature == nil || *request.Signature != (emailprovider.SignatureDetails{
		Name:              "Alex Morgan",
		Title:             "Partnerships Manager",
		AdditionalDetails: "Phone: +61 400 000 000\nAddress: 10 Current Street",
	}) {
		t.Fatalf("reply signature metadata = %#v", request.Signature)
	}
}

func TestInboxReplyFailsClosedWhenActiveSignatureIsUnavailable(t *testing.T) {
	sentinel := errors.New("active signature unavailable")
	service := &Service{repo: &activeSignatureReplyRepo{err: sentinel}}
	target := Message{
		ID:        uuid.New(),
		Direction: MessageDirectionInbound,
		FromEmail: "owner@restaurant.example",
		ToEmail:   "sales@tuvisolutions.com",
		Subject:   "Original subject",
	}

	request, err := service.prepareSignedInboxReply(
		context.Background(),
		target,
		ReplyMessageInput{BodyText: "Thanks for replying."},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("prepareSignedInboxReply() error = %v, want active signature error", err)
	}
	if request.To != "" || request.Subject != "" || request.TextBody != "" || request.HTMLBody != "" || request.Signature != nil {
		t.Fatalf("request = %#v, want empty request", request)
	}
}

func TestPrepareInboxReplyRejectsUnsafeInput(t *testing.T) {
	base := Message{
		ID:        uuid.New(),
		Direction: MessageDirectionInbound,
		FromEmail: "owner@example.com",
		Subject:   "Hello",
	}
	tests := []struct {
		name   string
		target Message
		input  ReplyMessageInput
	}{
		{name: "outbound target", target: Message{Direction: MessageDirectionOutbound}, input: ReplyMessageInput{BodyText: "reply"}},
		{name: "empty body", target: base, input: ReplyMessageInput{}},
		{name: "header injection", target: base, input: ReplyMessageInput{Subject: "safe\r\nBcc: bad@example.com", BodyText: "reply"}},
		{name: "invalid sender", target: Message{ID: uuid.New(), Direction: MessageDirectionInbound, FromEmail: "bad"}, input: ReplyMessageInput{BodyText: "reply"}},
		{name: "oversized body", target: base, input: ReplyMessageInput{BodyText: strings.Repeat("a", 10001)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := prepareInboxReply(test.target, test.input)
			if !errors.Is(err, ErrInvalidInboxReply) {
				t.Fatalf("prepareInboxReply() error = %v, want ErrInvalidInboxReply", err)
			}
		})
	}
}
