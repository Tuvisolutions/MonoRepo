package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/platform/config"
)

func TestLoadOutreachInboundPollIntervalDefaultsAndClampsToFifteenSeconds(t *testing.T) {
	t.Setenv("OUTREACH_INBOUND_POLL_SECONDS", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() default error = %v", err)
	}
	if cfg.Outreach.InboundPollInterval != 15*time.Second {
		t.Fatalf("default InboundPollInterval = %s, want 15s", cfg.Outreach.InboundPollInterval)
	}

	t.Setenv("OUTREACH_INBOUND_POLL_SECONDS", "5")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load() clamped error = %v", err)
	}
	if cfg.Outreach.InboundPollInterval != 15*time.Second {
		t.Fatalf("clamped InboundPollInterval = %s, want 15s", cfg.Outreach.InboundPollInterval)
	}
}

func TestLoadOutreachZohoAccountsJSON(t *testing.T) {
	t.Setenv("OUTREACH_ZOHO_ACCOUNTS_JSON", `[{"account_id":"acc1","from_email":"a@example.com","client_id":"cid","client_secret":"sec","refresh_token":"rt","region":"com"}]`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Outreach.ZohoAccounts) != 1 {
		t.Fatalf("ZohoAccounts len = %d, want 1", len(cfg.Outreach.ZohoAccounts))
	}
	if cfg.Outreach.ZohoAccounts[0].AccountID != "acc1" {
		t.Fatalf("AccountID = %q, want acc1", cfg.Outreach.ZohoAccounts[0].AccountID)
	}
}

func TestLoadOutreachGoogleWorkspaceAccountsJSON(t *testing.T) {
	t.Setenv("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON", `[{
		"key":"sales-au-1",
		"mailbox_email":"sales1@example.com",
		"client_id":"client-id",
		"client_secret":"client-secret",
		"refresh_token":"refresh-token"
	}]`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Outreach.GoogleWorkspaceAccounts) != 1 {
		t.Fatalf("GoogleWorkspaceAccounts len = %d, want 1", len(cfg.Outreach.GoogleWorkspaceAccounts))
	}
	account := cfg.Outreach.GoogleWorkspaceAccounts[0]
	if account.AccountKey != "sales-au-1" {
		t.Fatalf("AccountKey = %q, want sales-au-1", account.AccountKey)
	}
	if account.MailboxEmail != "sales1@example.com" {
		t.Fatalf("MailboxEmail = %q, want sales1@example.com", account.MailboxEmail)
	}
	if account.FromEmail != account.MailboxEmail {
		t.Fatalf("FromEmail = %q, want mailbox fallback %q", account.FromEmail, account.MailboxEmail)
	}
}

func TestLoadOutreachGoogleWorkspaceAccountsCanonicalizesMailbox(t *testing.T) {
	t.Setenv("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON", `[{
		"mailbox_email":"<Sales1@Example.com>",
		"client_id":"client-id",
		"client_secret":"client-secret",
		"refresh_token":"refresh-token"
	}]`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	account := cfg.Outreach.GoogleWorkspaceAccounts[0]
	if account.MailboxEmail != "sales1@example.com" || account.AccountKey != "sales1@example.com" {
		t.Fatalf("canonical mailbox/key = %q/%q, want sales1@example.com", account.MailboxEmail, account.AccountKey)
	}
}

func TestLoadOutreachGoogleWorkspaceAccountsRejectsInvalidMailbox(t *testing.T) {
	t.Setenv("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON", `[{
		"mailbox_email":"Sales Team <sales1@example.com>",
		"client_id":"client-id",
		"client_secret":"client-secret",
		"refresh_token":"refresh-token"
	}]`)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "mailbox_email") {
		t.Fatalf("Load() error = %v, want mailbox_email validation error", err)
	}
}

func TestLoadOutreachInboundUsesSelectedSendingAccount(t *testing.T) {
	t.Setenv("OUTREACH_INBOUND_ENABLED", "true")
	t.Setenv("OUTREACH_INBOUND_ACCOUNT_KEY", "outreach")
	t.Setenv("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON", `[
		{"key":"sales","mailbox_email":"sales@tuvisolutions.com","client_id":"client-id","client_secret":"client-secret","refresh_token":"refresh-token"},
		{"key":"outreach","mailbox_email":"outreach@tuvisolutions.com","client_id":"client-id","client_secret":"client-secret","refresh_token":"refresh-token"}
	]`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Outreach.InboundEnabled {
		t.Fatal("InboundEnabled = false")
	}
	if cfg.Outreach.InboundLocalPart != "outreach" || cfg.Outreach.InboundDomain != "tuvisolutions.com" {
		t.Fatalf("inbound address = %s@%s", cfg.Outreach.InboundLocalPart, cfg.Outreach.InboundDomain)
	}
	if cfg.Outreach.InboundMailbox == nil || cfg.Outreach.InboundMailbox.MailboxEmail != "outreach@tuvisolutions.com" {
		t.Fatalf("InboundMailbox = %#v", cfg.Outreach.InboundMailbox)
	}
	if cfg.Outreach.InboundMailbox.AccountKey != "outreach" {
		t.Fatalf("inbound AccountKey = %q, want outreach", cfg.Outreach.InboundMailbox.AccountKey)
	}
	if len(cfg.Outreach.InboundMailboxes) != 2 {
		t.Fatalf("InboundMailboxes len = %d, want every configured mailbox", len(cfg.Outreach.InboundMailboxes))
	}
	if cfg.Outreach.InboundMailboxes[0].AccountKey != "sales" || cfg.Outreach.InboundMailboxes[1].AccountKey != "outreach" {
		t.Fatalf("InboundMailboxes = %#v, want configured order", cfg.Outreach.InboundMailboxes)
	}
}

func TestLoadOutreachInboundMailboxJSONArray(t *testing.T) {
	t.Setenv("OUTREACH_INBOUND_ENABLED", "true")
	t.Setenv("OUTREACH_INBOUND_MAILBOX_JSON", `[
		{"key":"inbound","mailbox_email":"contact@tuvisolutions.com","client_id":"cid","client_secret":"sec","refresh_token":"rt"},
		{"key":"inbound-operations","mailbox_email":"operations@tuvisolutions.com","client_id":"cid","client_secret":"sec","refresh_token":"rt2"},
		{"key":"inbound-support","mailbox_email":"support@tuvisolutions.com","client_id":"cid","client_secret":"sec","refresh_token":"rt3"}
	]`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Outreach.InboundMailbox == nil || cfg.Outreach.InboundMailbox.MailboxEmail != "contact@tuvisolutions.com" {
		t.Fatalf("InboundMailbox = %#v", cfg.Outreach.InboundMailbox)
	}
	if len(cfg.Outreach.InboundMailboxes) != 3 {
		t.Fatalf("InboundMailboxes len = %d, want 3", len(cfg.Outreach.InboundMailboxes))
	}
	if cfg.Outreach.InboundMailboxes[1].MailboxEmail != "operations@tuvisolutions.com" || cfg.Outreach.InboundMailboxes[2].MailboxEmail != "support@tuvisolutions.com" {
		t.Fatalf("InboundMailboxes = %#v", cfg.Outreach.InboundMailboxes)
	}
}

func TestLoadOutreachInboundKeepsDedicatedMailboxAndPollsAllSendingAccounts(t *testing.T) {
	t.Setenv("OUTREACH_INBOUND_ENABLED", "true")
	t.Setenv("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON", `[
		{"key":"sales-one","mailbox_email":"sales1@tuvisolutions.com","client_id":"client-id","client_secret":"client-secret","refresh_token":"refresh-token"},
		{"key":"sales-two","mailbox_email":"sales2@tuvisolutions.com","client_id":"client-id","client_secret":"client-secret","refresh_token":"refresh-token"}
	]`)
	t.Setenv("OUTREACH_INBOUND_MAILBOX_JSON", `{
		"key":"inbound",
		"mailbox_email":"inbox@tuvisolutions.com",
		"client_id":"inbound-client-id",
		"client_secret":"inbound-client-secret",
		"refresh_token":"inbound-refresh-token"
	}`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Outreach.InboundMailbox == nil || cfg.Outreach.InboundMailbox.AccountKey != "inbound" {
		t.Fatalf("InboundMailbox = %#v", cfg.Outreach.InboundMailbox)
	}
	if cfg.Outreach.InboundLocalPart != "inbox" || cfg.Outreach.InboundDomain != "tuvisolutions.com" {
		t.Fatalf("inbound reply address = %s@%s", cfg.Outreach.InboundLocalPart, cfg.Outreach.InboundDomain)
	}
	if len(cfg.Outreach.InboundMailboxes) != 3 {
		t.Fatalf("InboundMailboxes len = %d, want two senders plus dedicated inbox", len(cfg.Outreach.InboundMailboxes))
	}
	if cfg.Outreach.InboundMailboxes[2].AccountKey != "inbound" {
		t.Fatalf("InboundMailboxes = %#v", cfg.Outreach.InboundMailboxes)
	}
}

func TestLoadOutreachInboundRejectsDedicatedMailboxKeyConflict(t *testing.T) {
	t.Setenv("OUTREACH_INBOUND_ENABLED", "true")
	t.Setenv("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON", `[{
		"key":"inbound",
		"mailbox_email":"sales@tuvisolutions.com",
		"client_id":"client-id",
		"client_secret":"client-secret",
		"refresh_token":"refresh-token"
	}]`)
	t.Setenv("OUTREACH_INBOUND_MAILBOX_JSON", `{
		"key":"inbound",
		"mailbox_email":"inbox@tuvisolutions.com",
		"client_id":"inbound-client-id",
		"client_secret":"inbound-client-secret",
		"refresh_token":"inbound-refresh-token"
	}`)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "conflicts with a different configured mailbox") {
		t.Fatalf("Load() error = %v, want dedicated mailbox key conflict", err)
	}
}

func TestLoadOutreachInboundReusesSenderKeyForSameDedicatedMailbox(t *testing.T) {
	t.Setenv("OUTREACH_INBOUND_ENABLED", "true")
	t.Setenv("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON", `[{
		"key":"sales",
		"mailbox_email":"sales@tuvisolutions.com",
		"client_id":"send-client-id",
		"client_secret":"send-client-secret",
		"refresh_token":"send-refresh-token"
	}]`)
	t.Setenv("OUTREACH_INBOUND_MAILBOX_JSON", `{
		"key":"inbound",
		"mailbox_email":"sales@tuvisolutions.com",
		"client_id":"read-client-id",
		"client_secret":"read-client-secret",
		"refresh_token":"read-refresh-token"
	}`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Outreach.InboundMailbox == nil || cfg.Outreach.InboundMailbox.AccountKey != "sales" {
		t.Fatalf("InboundMailbox = %#v, want sender key", cfg.Outreach.InboundMailbox)
	}
	if len(cfg.Outreach.InboundMailboxes) != 1 {
		t.Fatalf("InboundMailboxes len = %d, want same mailbox deduplicated", len(cfg.Outreach.InboundMailboxes))
	}
	mailbox := cfg.Outreach.InboundMailboxes[0]
	if mailbox.AccountKey != "sales" || mailbox.RefreshToken != "read-refresh-token" {
		t.Fatalf("InboundMailboxes[0] = %#v, want sender key with dedicated read credentials", mailbox)
	}
}

func TestLoadOutreachInboundRequiresSendingAccountWhenEnabled(t *testing.T) {
	t.Setenv("OUTREACH_INBOUND_ENABLED", "true")
	t.Setenv("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON", "")

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON") {
		t.Fatalf("Load() error = %v, want inbound mailbox required", err)
	}
}

func TestLoadOutreachInboundRejectsUnknownAccountKey(t *testing.T) {
	t.Setenv("OUTREACH_INBOUND_ENABLED", "true")
	t.Setenv("OUTREACH_INBOUND_ACCOUNT_KEY", "missing")
	t.Setenv("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON", `[{
		"key":"outreach",
		"mailbox_email":"outreach@tuvisolutions.com",
		"client_id":"client-id",
		"client_secret":"client-secret",
		"refresh_token":"refresh-token"
	}]`)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "OUTREACH_INBOUND_ACCOUNT_KEY") {
		t.Fatalf("Load() error = %v, want unknown inbound account error", err)
	}
}
