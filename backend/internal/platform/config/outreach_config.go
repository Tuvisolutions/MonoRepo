package config

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

type outreachZohoAccountJSON struct {
	Key          string `json:"key"`
	AccountID    string `json:"account_id"`
	FromEmail    string `json:"from_email"`
	Region       string `json:"region"`
	APIBaseURL   string `json:"api_base_url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

type outreachGoogleWorkspaceAccountJSON struct {
	Key          string `json:"key"`
	MailboxEmail string `json:"mailbox_email"`
	FromEmail    string `json:"from_email"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

func loadOutreachConfig(parser *envParser) OutreachConfig {
	cfg := OutreachConfig{
		BulkMax:                     parser.int("OUTREACH_BULK_MAX", 150),
		EmailsPerAccount:            parser.int("OUTREACH_EMAILS_PER_ACCOUNT", 40),
		SendWindow:                  parser.duration("OUTREACH_SEND_WINDOW", 8*time.Hour),
		SendJitterMin:               parser.duration("OUTREACH_SEND_JITTER_MIN", 2*time.Minute),
		SendJitterMax:               parser.duration("OUTREACH_SEND_JITTER_MAX", 5*time.Minute),
		AccountCooldown:             parser.duration("OUTREACH_EMAIL_COOLDOWN", 24*time.Hour),
		EmailHealthEnabled:          parser.bool("OUTREACH_EMAIL_HEALTH_ENABLED", true),
		EmailHealthRecipient:        parser.string("OUTREACH_EMAIL_HEALTH_RECIPIENT", "rajchodisetti@gmail.com"),
		EmailHealthInterval:         parser.duration("OUTREACH_EMAIL_HEALTH_INTERVAL", 24*time.Hour),
		ZohoAccountsJSON:            parser.string("OUTREACH_ZOHO_ACCOUNTS_JSON", ""),
		GoogleWorkspaceAccountsJSON: parser.string("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON", ""),
		CredentialEncryptionKey:     parser.string("OUTREACH_CREDENTIAL_ENCRYPTION_KEY", ""),
		InboundEnabled:              parser.bool("OUTREACH_INBOUND_ENABLED", false),
		InboundAccountKey:           strings.TrimSpace(parser.string("OUTREACH_INBOUND_ACCOUNT_KEY", "")),
		InboundMailboxJSON:          parser.string("OUTREACH_INBOUND_MAILBOX_JSON", ""),
		InboundPollInterval:         time.Duration(parser.int("OUTREACH_INBOUND_POLL_SECONDS", 15)) * time.Second,
	}

	if cfg.InboundPollInterval < 15*time.Second {
		cfg.InboundPollInterval = 15 * time.Second
	}
	keys := make(map[string]struct{})
	loadOutreachZohoAccounts(parser, &cfg, keys)
	loadOutreachGoogleWorkspaceAccounts(parser, &cfg, keys)
	loadOutreachInboundAccount(parser, &cfg)
	return cfg
}

func loadOutreachZohoAccounts(parser *envParser, cfg *OutreachConfig, keys map[string]struct{}) {
	raw := strings.TrimSpace(cfg.ZohoAccountsJSON)
	if raw == "" {
		return
	}

	var entries []outreachZohoAccountJSON
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		parser.addError(fmt.Errorf("OUTREACH_ZOHO_ACCOUNTS_JSON must be valid JSON array: %w", err))
		return
	}

	identities := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		accountKey := strings.TrimSpace(entry.Key)
		if accountKey == "" {
			accountKey = strings.TrimSpace(entry.AccountID)
		}
		account := ZohoMailConfig{
			AccountKey:   accountKey,
			AccountID:    strings.TrimSpace(entry.AccountID),
			FromEmail:    strings.TrimSpace(entry.FromEmail),
			Region:       strings.TrimSpace(entry.Region),
			APIBaseURL:   strings.TrimSpace(entry.APIBaseURL),
			ClientID:     strings.TrimSpace(entry.ClientID),
			ClientSecret: strings.TrimSpace(entry.ClientSecret),
			RefreshToken: strings.TrimSpace(entry.RefreshToken),
		}
		if account.Region == "" {
			account.Region = "com"
		}
		if account.APIBaseURL == "" {
			account.APIBaseURL = "https://mail.zoho.com/api/accounts"
		}
		if account.AccountKey == "" || account.AccountID == "" || account.ClientID == "" || account.ClientSecret == "" || account.RefreshToken == "" {
			parser.addError(fmt.Errorf("OUTREACH_ZOHO_ACCOUNTS_JSON entry %d is missing required Zoho fields", index+1))
			continue
		}
		if _, exists := keys[account.AccountKey]; exists {
			parser.addError(fmt.Errorf("OUTREACH_ZOHO_ACCOUNTS_JSON entry %d has duplicate key %q", index+1, account.AccountKey))
			continue
		}
		identity := strings.ToLower(account.Region) + "|" + strings.ToLower(account.AccountID)
		if _, exists := identities[identity]; exists {
			parser.addError(fmt.Errorf("OUTREACH_ZOHO_ACCOUNTS_JSON entry %d duplicates Zoho account %q in region %q", index+1, account.AccountID, account.Region))
			continue
		}
		keys[account.AccountKey] = struct{}{}
		identities[identity] = struct{}{}
		cfg.ZohoAccounts = append(cfg.ZohoAccounts, account)
	}
}

func loadOutreachGoogleWorkspaceAccounts(parser *envParser, cfg *OutreachConfig, keys map[string]struct{}) {
	raw := strings.TrimSpace(cfg.GoogleWorkspaceAccountsJSON)
	if raw == "" {
		return
	}

	var entries []outreachGoogleWorkspaceAccountJSON
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		parser.addError(fmt.Errorf("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON must be valid JSON array: %w", err))
		return
	}

	identities := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		mailboxEmail, err := canonicalOutreachMailbox(entry.MailboxEmail)
		if err != nil {
			parser.addError(fmt.Errorf("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON entry %d mailbox_email: %w", index+1, err))
			continue
		}
		fromEmail := mailboxEmail
		if strings.TrimSpace(entry.FromEmail) != "" {
			fromEmail, err = canonicalOutreachMailbox(entry.FromEmail)
			if err != nil {
				parser.addError(fmt.Errorf("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON entry %d from_email: %w", index+1, err))
				continue
			}
		}
		if fromEmail == "" {
			fromEmail = mailboxEmail
		}
		accountKey := strings.TrimSpace(entry.Key)
		if accountKey == "" {
			accountKey = mailboxEmail
		}
		account := GmailMailConfig{
			AccountKey:   accountKey,
			MailboxEmail: mailboxEmail,
			FromEmail:    fromEmail,
			ClientID:     strings.TrimSpace(entry.ClientID),
			ClientSecret: strings.TrimSpace(entry.ClientSecret),
			RefreshToken: strings.TrimSpace(entry.RefreshToken),
		}
		if account.AccountKey == "" || account.MailboxEmail == "" || account.FromEmail == "" || account.ClientID == "" || account.ClientSecret == "" || account.RefreshToken == "" {
			parser.addError(fmt.Errorf("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON entry %d is missing required Google Workspace fields", index+1))
			continue
		}
		if _, exists := keys[account.AccountKey]; exists {
			parser.addError(fmt.Errorf("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON entry %d has duplicate key %q", index+1, account.AccountKey))
			continue
		}
		if _, exists := identities[account.MailboxEmail]; exists {
			parser.addError(fmt.Errorf("OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON entry %d duplicates Google Workspace mailbox %q", index+1, account.MailboxEmail))
			continue
		}
		keys[account.AccountKey] = struct{}{}
		identities[account.MailboxEmail] = struct{}{}
		cfg.GoogleWorkspaceAccounts = append(cfg.GoogleWorkspaceAccounts, account)
	}
}

func loadOutreachInboundAccount(parser *envParser, cfg *OutreachConfig) {
	if !cfg.InboundEnabled {
		return
	}
	cfg.InboundMailboxes = append([]GmailMailConfig(nil), cfg.GoogleWorkspaceAccounts...)
	if raw := strings.TrimSpace(cfg.InboundMailboxJSON); raw != "" {
		entries, err := parseInboundMailboxJSON(raw)
		if err != nil {
			parser.addError(err)
			return
		}
		var selected *GmailMailConfig
		for index, entry := range entries {
			account, accountErr := inboundAccountFromJSON(entry, index)
			if accountErr != nil {
				parser.addError(accountErr)
				continue
			}
			account, ok := addInboundPollingMailbox(parser, cfg, account)
			if !ok {
				continue
			}
			if selected == nil {
				copy := account
				selected = &copy
			}
		}
		if selected != nil {
			assignInboundMailbox(parser, cfg, *selected)
		}
		return
	}
	if len(cfg.GoogleWorkspaceAccounts) == 0 {
		parser.addError(fmt.Errorf("OUTREACH_INBOUND_MAILBOX_JSON or OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON is required when OUTREACH_INBOUND_ENABLED is true"))
		return
	}
	selected := cfg.GoogleWorkspaceAccounts[0]
	if cfg.InboundAccountKey != "" {
		found := false
		for _, account := range cfg.GoogleWorkspaceAccounts {
			if account.AccountKey == cfg.InboundAccountKey {
				selected = account
				found = true
				break
			}
		}
		if !found {
			parser.addError(fmt.Errorf("OUTREACH_INBOUND_ACCOUNT_KEY %q does not match a configured Google Workspace account", cfg.InboundAccountKey))
			return
		}
	}
	assignInboundMailbox(parser, cfg, selected)
}

func parseInboundMailboxJSON(raw string) ([]outreachGoogleWorkspaceAccountJSON, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "[") {
		var entries []outreachGoogleWorkspaceAccountJSON
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			return nil, fmt.Errorf("OUTREACH_INBOUND_MAILBOX_JSON must be a valid JSON object or array: %w", err)
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("OUTREACH_INBOUND_MAILBOX_JSON array must contain at least one mailbox")
		}
		return entries, nil
	}

	var entry outreachGoogleWorkspaceAccountJSON
	if err := json.Unmarshal([]byte(trimmed), &entry); err != nil {
		return nil, fmt.Errorf("OUTREACH_INBOUND_MAILBOX_JSON must be a valid JSON object or array: %w", err)
	}
	return []outreachGoogleWorkspaceAccountJSON{entry}, nil
}

func inboundAccountFromJSON(entry outreachGoogleWorkspaceAccountJSON, index int) (GmailMailConfig, error) {
	prefix := "OUTREACH_INBOUND_MAILBOX_JSON"
	if index >= 0 {
		prefix = fmt.Sprintf("OUTREACH_INBOUND_MAILBOX_JSON entry %d", index+1)
	}
	mailboxEmail, err := canonicalOutreachMailbox(entry.MailboxEmail)
	if err != nil {
		return GmailMailConfig{}, fmt.Errorf("%s mailbox_email: %w", prefix, err)
	}
	fromEmail := mailboxEmail
	if strings.TrimSpace(entry.FromEmail) != "" {
		fromEmail, err = canonicalOutreachMailbox(entry.FromEmail)
		if err != nil {
			return GmailMailConfig{}, fmt.Errorf("%s from_email: %w", prefix, err)
		}
	}
	accountKey := strings.TrimSpace(entry.Key)
	if accountKey == "" {
		if index <= 0 {
			accountKey = "inbound"
		} else {
			accountKey = "inbound-" + mailboxEmail
		}
	}
	account := GmailMailConfig{
		AccountKey:   accountKey,
		MailboxEmail: mailboxEmail,
		FromEmail:    fromEmail,
		ClientID:     strings.TrimSpace(entry.ClientID),
		ClientSecret: strings.TrimSpace(entry.ClientSecret),
		RefreshToken: strings.TrimSpace(entry.RefreshToken),
	}
	if account.ClientID == "" || account.ClientSecret == "" || account.RefreshToken == "" {
		return GmailMailConfig{}, fmt.Errorf("%s is missing required Google Workspace fields", prefix)
	}
	return account, nil
}

func addInboundPollingMailbox(parser *envParser, cfg *OutreachConfig, account GmailMailConfig) (GmailMailConfig, bool) {
	for index, existing := range cfg.InboundMailboxes {
		if existing.AccountKey == account.AccountKey {
			if existing.MailboxEmail != account.MailboxEmail {
				parser.addError(fmt.Errorf("OUTREACH_INBOUND_MAILBOX_JSON key %q conflicts with a different configured mailbox", account.AccountKey))
				return GmailMailConfig{}, false
			}
			cfg.InboundMailboxes[index] = account
			return account, true
		}
		if existing.MailboxEmail == account.MailboxEmail {
			// The dedicated inbox can carry a read-scoped token for a mailbox that
			// already sends outreach. Keep the sender's durable key so inbound and
			// outbound messages remain in one thread, while using the dedicated
			// credentials for inbox polling.
			account.AccountKey = existing.AccountKey
			cfg.InboundMailboxes[index] = account
			return account, true
		}
	}
	cfg.InboundMailboxes = append(cfg.InboundMailboxes, account)
	return account, true
}

func assignInboundMailbox(parser *envParser, cfg *OutreachConfig, selected GmailMailConfig) {
	at := strings.LastIndex(selected.MailboxEmail, "@")
	if at <= 0 || at == len(selected.MailboxEmail)-1 {
		parser.addError(fmt.Errorf("selected outreach inbox mailbox_email is invalid"))
		return
	}
	cfg.InboundAccountKey = selected.AccountKey
	cfg.InboundLocalPart = strings.ToLower(selected.MailboxEmail[:at])
	cfg.InboundDomain = strings.ToLower(selected.MailboxEmail[at+1:])
	account := selected
	cfg.InboundMailbox = &account
}

func canonicalOutreachMailbox(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("email address is required")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || strings.TrimSpace(address.Address) == "" || address.Name != "" {
		return "", fmt.Errorf("email address is invalid")
	}
	return strings.ToLower(strings.TrimSpace(address.Address)), nil
}
