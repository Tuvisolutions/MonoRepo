# Lead scrape and outreach operations

This runbook covers the production path from Australian restaurant discovery
to the approved plain-text email sequence. Image OCR is retired and is not a
lead-eligibility or outreach dependency.

## Runtime topology

- `scrape-worker` claims durable city jobs and uses Google Places first.
- Apollo may add missing owner and work-email details, but missing credentials,
  no-match, and provider failures do not mark the job failed or stop verified
  Places data from importing. The shared request ceiling still pauses all
  provider work when exhausted.
- `import_to_db.py` upserts the restaurant/profile and records the lead as
  `inferred_business` with source evidence.
- `ensure_outreach_sequence_enrollment(uuid)` enrolls any restaurant with a
  non-empty name, valid email, eligible lifecycle, and recorded inferred-business
  consent in the active approved sequence. It does not consult legacy suppressions.
- The Go worker sends only when the persisted outreach job is enabled.
- Gmail mailbox quotas, idempotency, and delivery-attempt records remain
  authoritative.

There is no OCR container, one-shot OCR job, host cron, or OCR provider key.

## Eligibility and lifecycle

Eligible lifecycle states are `lead` and `emailed`. A restaurant is excluded
or paused when it has expressed interest or is in `lost`, `archived`,
`client_onboarding`, or `active_client`. The application does not consult the
legacy email-suppression table.

Outreach does not require a generated profile, published demo, approved
restaurant-specific campaign, or media review. Media remains a separate safety
gate: Google media is resolved live with attribution, and owner/licensed files
need explicit admin approval before public use.

## Sequence behavior

- The active approved version may contain any positive number of steps.
- Seed data contains three approved Tuvi Solutions messages.
- Each step is plain text and is rendered from its saved PostgreSQL template.
- `{{greeting01}}` is optional, deterministic, and allowed exactly once in the
  first enabled email body. It is forbidden in subjects and later emails;
  `{{greeting}}` remains supported for follow-ups and legacy active sequences.
- A selected restaurant greeting uses city, the first safe cuisine ending in
  `Restaurant`, rating, and review count only when its profile has a Google
  place id and `scrape_status = 'success'`. Missing or unsafe optional facts
  always select a generic fallback and never block delivery.
- Preview and test-send accept an optional `restaurant_id`. The server then
  ignores synthetic name/owner fields, renders authoritative facts, and returns
  only `greeting01` plus non-sensitive fact-category names for review.
- Scheduled sends keep each in-progress recipient's subject and body pinned to
  its enrolled sequence version, but resolve the signature from the current
  active approved version immediately before preparing the provider request.
  Inbox replies use that same active signature. If it cannot be loaded, both
  paths fail closed before a provider call. A test send for an explicitly
  selected sequence version continues to use that selected version's signature.
- Any unsubscribe copy or URL must be authored in that saved template. The
  application does not append, require, validate, or render a specialized
  unsubscribe merge tag.
- Delay is measured from the previous confirmed delivery; seed delays are 0,
  3, and 3 days.
- Due follow-ups are ordered before new recipients, but neither due nor future
  follow-ups remove new recipients from the eligible set.
- A normalized email used by more than three restaurant records is excluded at
  selection and rechecked immediately before delivery. The Restaurants admin
  page lists every shared-email group and its restaurant records.
- A failure or unknown provider result never advances the integer step.
- The local/unreleased safe-retry policy admits only a definitive
  `gmail_rate_limit_rejected`, `gmail_pre_send_unavailable`, or
  `credential_or_authorization_rejected` failure. It preserves that exact
  campaign step and schedules it no earlier than the next saved
  `Australia/Sydney` send window, preserving any later campaign hold, with at
  most three total attempts for the campaign step. This policy
  is not deployed until the matching application release is explicitly
  approved.
- Unknown, sent, sending, and non-allowlisted failures never enter this
  automatic retry path. Skipped outcomes are not admitted to the new
  definitive-failure scheduler or its cap; established skip handling remains
  separate. The persisted runtime control and every claim-time eligibility gate
  remain authoritative, including the temporary step 2/3 pause.
- A confirmed send advances the step and records sent/next-due timestamps.
- Future-due work is deferred; it does not disable the email job.

## Safe deployment order

1. Keep the production email job disabled.
2. Back up the database and protected environment files without printing
   secret values.
3. Confirm no retired OCR process/container/cron entry exists.
4. Apply the next sequential migration.
5. Deploy API, worker, admin, website, template, and scrape-worker images from
   the same release.
6. Verify migration version, health endpoints, Compose topology, and that no
   executable OCR references remain.
7. Run preview/fake-provider sequence tests. Do not send a real lead email.
8. Inspect eligible/follow-up counts and sequence rendering in the admin UI.
9. Enabling production outreach is a separate deliberate admin action.

Migration `000047_deterministic_restaurant_greeting` creates an inactive draft
cloned from the active sequence. Review and explicitly approve that draft in a
separate administrator action; applying the migration alone does not change the
active version or enable sending. Its down migration refuses to remove a draft
that has been edited or activated.

Migration `000050_reconcile_outreach_enrollment` replaces the stale
suppression-gated enrollment function, backfills missing eligible enrollments,
and leaves the email job disabled. Applying it never activates sending.

Migration `000052_outreach_email_credentials` adds encrypted, database-managed
Gmail accounts. Configure `OUTREACH_CREDENTIAL_ENCRYPTION_KEY` as standard
base64 encoding of exactly 32 random bytes before using the admin form. The down
migration refuses to remove the table while any encrypted credentials remain.

## Add or manage Gmail accounts from the admin UI

Use **Outreach → Email accounts** to add another Google Workspace mailbox.
Supply a stable lowercase account key, mailbox/from address, OAuth client ID,
client secret, and a mailbox refresh token authorized for `gmail.send` and
`gmail.readonly`. The API encrypts the complete credential set before database
storage and never returns it. Database accounts can be enabled, disabled, or
have their complete credential set replaced; account key and mailbox identity
remain immutable for audit and conversation continuity.

Every listed account has a **Replace credentials** action. For an existing
environment account, that action saves a database row with the exact same
account key and mailbox. The database row immediately becomes authoritative;
partial conflicts (matching only the key or only the mailbox) are rejected.
Existing environment secrets are never read into the browser or copied into the
database.

A definitive OAuth credential rejection (for example an expired/revoked grant,
invalid client/scope, Gmail 401, or a non-rate-limit Gmail 403 response) is known
to occur before Gmail accepts the message. Direct template tests skip that
account for the complete test batch and try the next configured sender.
Scheduled delivery records the attempt as failed with
`credential_or_authorization_rejected` and excludes the account from later
claims with the dedicated health quarantine.

Gmail 429, and Gmail 403 responses whose structured reason is
`userRateLimitExceeded`, `rateLimitExceeded`, or `dailyLimitExceeded`, are
separate definitive pre-acceptance rejections. The local/unreleased recovery
policy records `gmail_rate_limit_rejected` and cools that quota account until the
next Sydney window; it does not mark valid credentials unhealthy. A transient
OAuth token failure before the Gmail message endpoint is called records
`gmail_pre_send_unavailable` and follows the same bounded scheduler. Network
errors, provider 5xx responses, malformed success responses, and other ambiguous
outcomes from the Gmail message endpoint remain `unknown` and are never retried
automatically because Gmail may have accepted the message.

An enabled due health check that succeeds clears the dedicated quarantine.
For database-managed accounts, replacing credentials, correcting the From
address, or explicitly re-enabling a disabled account also clears only this
credential/authorization quarantine atomically. None of these recovery actions
enables the bulk email job, and a disabled health-check control remains disabled.

The effective runtime list is the union of
`OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON`, an optional dedicated inbound mailbox,
and enabled database accounts. A database identity always takes precedence over
the same environment identity. A disabled or unreadable database override fails
closed: the environment secret is not used as a fallback. Sending, health
registration, and each inbox poll reload this effective list, so a UI change
does not require a restart. Disabling an account preserves its messages, quota
history, and sync history but excludes it from new sends and polls. Adding or
replacing an account does not enable the bulk email job.

## Review daily scheduled send history

Use **Outreach → Send history** to inspect one `Australia/Sydney` calendar date.
The summary shows the full-day attempt counts for every configured From address,
including sent totals split across Email 1, Email 2, Email 3, and legacy/later
steps. Select **View recipients** for an email ID to filter the detail list by
that stable sender account. Each row shows the attempt time, restaurant,
immutable recipient address, sequence email/phase, stored outbound subject when
available, controlled outcome, and provider message identifier when one was
recorded.

The screen is an attempt ledger, not an inbox-delivery report. `Sent` means the
attempt is currently marked sent after provider acceptance; it does not prove
recipient inbox delivery. Failed, ambiguous, skipped, and in-progress attempts
remain visible so the sender totals reconcile. A later bounce appears as failed
only after a separate reconciliation records it. Raw provider responses and
message bodies are never returned by this endpoint.

Template test sends, manual inbox replies, health checks, consultation messages,
and other direct email paths are not quota-managed delivery attempts and are not
included in this screen.

## Recover definitively rejected scheduled sends

> **Local/unreleased:** This automatic recovery policy is accepted in the
> [safe failed-outreach retry ADR](../adr/2026-08-19-safe-failed-outreach-retry.md)
> but does not change production until the matching API and worker build is
> explicitly approved and deployed.

Do not reset a restaurant's contact/email fields and do not refund or reset an
email account's usage, cycle, or ramp counters. Those values preserve confirmed
contact and reserved provider capacity; they are not the failed-delivery queue.

The worker may requeue only the exact campaign step whose immutable attempt is
`failed` with one of these controlled codes:

- `gmail_rate_limit_rejected`: set the selected quota account's
  `available_at` no earlier than the next Sydney window start. Do not create a
  credential-health quarantine.
- `gmail_pre_send_unavailable`: the OAuth token/pre-send stage failed before the
  Gmail message endpoint was called. Apply the same bounded next-window retry;
  cancellation before that endpoint is also safe, while cancellation at the
  message endpoint remains ambiguous.
- `credential_or_authorization_rejected`: keep the dedicated account health
  quarantine. The campaign may use another healthy mailbox when it becomes due.

The campaign retains its existing step and becomes due no earlier than the next
local calendar day's saved `Australia/Sydney` window start. Any later campaign
hold remains in force. The conversion uses the IANA timezone, so the local start
remains correct across daylight-saving changes. Each retry consumes a fresh
quota slot and creates a new immutable attempt. The cap is three non-skipped
provider-boundary attempts for one `(campaign_id, campaign_step)`; established
skipped/redirected safety attempts do not consume this failure cap. A retained
`sent`, `sending`, or `unknown` outcome discovered while finalizing a new failed
attempt stops recovery with `delivery_outcome_conflict`. If an inconsistent row
was already manually reopened, list/claim gates leave it paused without another
provider call until an operator reconciles it. Another allowlisted failure on
the third counted attempt stops the campaign with `delivery_retry_exhausted`. A
failed code outside the allowlist stops with
`delivery_failure_not_retryable`. The same list and claim gates pause a
historical non-allowlisted failure if its campaign was manually reopened; this
includes an accepted-then-bounced attempt.

Recovery does not enable `email_job`, bypass account health, or override
lifecycle, interest, consent, sequence approval, enabled-step, schedule, pacing,
quota, or idempotency checks. Keep the job disabled through deployment and
verification. Enabling it is a separate production mutation requiring explicit
approval.

On an admitted failure, the same database transaction resets only the persisted
attempt count of the exact running bulk job linked to that delivery. It does not
change the job's status, owner, lease, payload, or availability. The live worker
therefore completes its normal fenced deferral; after a process crash, lease
expiry can reclaim the otherwise one-attempt job. This is job crash recovery,
not permission to retry an `unknown` provider outcome.

Review the affected Sydney date under **Outreach → Send history**. Filter by
sender and confirm the failed row's controlled error code. Use aggregate queries
only when database evidence is needed:

```sql
WITH sydney_day AS (
  SELECT
    date_trunc('day', now() AT TIME ZONE 'Australia/Sydney')
      AT TIME ZONE 'Australia/Sydney' AS starts_at,
    (date_trunc('day', now() AT TIME ZONE 'Australia/Sydney') + interval '1 day')
      AT TIME ZONE 'Australia/Sydney' AS ends_at
)
SELECT account.account_key, attempt.status, attempt.error_code, count(*)
FROM email_delivery_attempts AS attempt
JOIN outreach_email_accounts AS account ON account.id = attempt.account_id
CROSS JOIN sydney_day
WHERE attempt.created_at >= sydney_day.starts_at
  AND attempt.created_at < sydney_day.ends_at
GROUP BY account.account_key, attempt.status, attempt.error_code
ORDER BY account.account_key, attempt.status, attempt.error_code;

SELECT event_type, COALESCE(metadata->>'error_code', '') AS error_code, count(*)
FROM email_events
WHERE event_type IN ('retry_scheduled', 'retry_exhausted')
  AND event_time >= date_trunc('day', now() AT TIME ZONE 'Australia/Sydney')
      AT TIME ZONE 'Australia/Sydney'
  AND event_time < (date_trunc('day', now() AT TIME ZONE 'Australia/Sydney') + interval '1 day')
      AT TIME ZONE 'Australia/Sydney'
GROUP BY event_type, COALESCE(metadata->>'error_code', '')
ORDER BY event_type, error_code;
```

Escalate for operator reconciliation when an attempt is `unknown`, a sending
lease ages into `unknown`, a failure code is outside the allowlist, or the cap is
exhausted. Never infer a retry from a message missing in Gmail Sent. A message
accepted by Gmail and later bounced requires an authenticated structured DSN or
authoritative provider event that correlates to exactly one immutable attempt;
free-form bounce text and mailbox visibility are insufficient. The current
unreleased recovery path does not auto-retry accepted-then-bounced messages.

## Unified inbox across configured sending mailboxes

Set `OUTREACH_INBOUND_ENABLED=true` and optionally
`OUTREACH_INBOUND_MAILBOX_JSON` with one dedicated Gmail account. When the
dedicated object is present, it defines the canonical plus-address Reply-To and
is polled alongside every entry in `OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON`.
It is registered for manual same-mailbox replies but is not added to the bulk
quota rotation. If it duplicates a sender mailbox under another key, the worker
uses the sender's durable key and the dedicated read credential so messages are
not fetched twice or split into separate conversations.

Without a dedicated object, `OUTREACH_INBOUND_ACCOUNT_KEY` must select an entry already present in
`OUTREACH_GOOGLE_WORKSPACE_ACCOUNTS_JSON`; when omitted, the first effective
environment-or-database sending entry defines the canonical plus-address
Reply-To. Every effective Google Workspace account is polled independently and
every refresh token needs both `gmail.send` and `gmail.readonly`.

The initial and fallback sync uses `in:inbox newer_than:10d`; the API also
filters on Gmail's provider-received timestamp, so only the last 10 days are
shown. Older stored snapshots are retained. Each mailbox has its own history
cursor and last-attempt/success/error state. The admin Inbox can combine all
mailboxes or filter by stable account key, and unmatched messages remain
replyable without pausing a restaurant campaign.

The admin Inbox reply action sends plain text from the mailbox that captured the
message, appends the current active approved sequence signature, preserves the
Gmail thread and RFC reply headers, and stores the accepted outbound snapshot.
It does not resume the stopped sequence.

## Resume a failed scrape job

The Scrape jobs admin screen shows a deliberate **Resume** action only for a
failed job. Confirming it calls `POST /api/v1/scrape-jobs/{id}/resume` and
requeues the same job. It preserves completed/subdivided cells, imported and
duplicate candidates, Places detail checkpoints, total request accounting, and
the current request window when it is less than 24 hours old. Only interrupted
or explicitly failed work is made pending again. If another active job exists
for the same city and niche, resume fails closed with HTTP 409.

Do not use Resume as a provider smoke test. It is an explicit production
mutation and can cause the worker to make real Places/Apollo calls once it
claims the queued job.

## Operational checks

Use redacted/aggregate queries only:

```sql
SELECT status, count(*) FROM restaurants GROUP BY status ORDER BY status;

SELECT outreach_consent_basis, count(*)
FROM restaurants
GROUP BY outreach_consent_basis
ORDER BY outreach_consent_basis;

SELECT sequence.status, count(*)
FROM email_campaigns AS campaign
JOIN outreach_email_sequences AS sequence ON sequence.id = campaign.sequence_id
GROUP BY sequence.status
ORDER BY sequence.status;

SELECT count(*)
FROM email_campaigns AS campaign
JOIN restaurants AS restaurant ON restaurant.id = campaign.restaurant_id
WHERE campaign.next_send_at <= now()
  AND campaign.status = 'approved'
  AND campaign.next_step IS NOT NULL
  AND restaurant.status IN ('lead', 'emailed')
  AND restaurant.shown_interest = false;
```

Confirm the outreach job remains disabled after deployment unless a human has
explicitly approved enabling it. Do not use a live provider for smoke tests.

## Rollback

Disable `email_job` before rolling back application images. The bounded
failed-delivery retry policy has no migration; preserve its attempts, events,
campaign timestamps, account cooldowns, and credential quarantines. Review any
campaign with retry-scheduled or retry-exhausted history before a prior worker is
enabled, and never mass-reset restaurant delivery fields or mailbox counters.

For a schema-bearing release, apply the matching migration down only if no
sequence sends have advanced under that schema. Historical OCR columns remain
during the stabilization window so the schema rollback is reversible; the
retired provider credentials are not restored automatically.
