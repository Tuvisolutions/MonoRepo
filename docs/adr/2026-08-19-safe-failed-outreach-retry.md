# ADR: Bounded next-day retry for definitively rejected outreach

Date: 2026-08-19
Status: Accepted (deployed in release `82f366f` on 2026-08-20)

Supersedes the Gmail 403 classification portion of the
[Gmail outreach and health ADR](2026-07-18-gmail-outreach-and-health.md). The
durable quota, Sydney schedule, and database-owned unsubscribe decisions remain
authoritative.

## Context

Quota-managed outreach already records an immutable attempt before crossing the
provider boundary. A timeout, network failure, provider 5xx response, or
malformed success response from the Gmail message endpoint can therefore be
ambiguous: Gmail may have accepted the message even when Tuvi cannot prove it.
Retrying those attempts risks contacting the same restaurant twice. A failure
while acquiring the OAuth token is different because the message endpoint has
provably not been called.

Some failures are different. Gmail can explicitly reject a request before
acceptance because the sender has reached a rate limit or because its credential
or authorization is unusable. Leaving the campaign immediately due can repeat
the same failure during the current send window. Resetting restaurant delivery
fields or mailbox counters would be worse: those values are confirmed-send
history and a durable reservation ledger, not queue controls.

Gmail also documents successful API acceptance separately from eventual inbox
delivery. A message missing from a Sent-folder view, or a later free-form bounce
message, is not sufficient proof that an accepted attempt is safe to send again.

## Decision

Automatic recovery operates on the exact `(campaign_id, campaign_step)`, not on
the restaurant row and not on a mailbox quota cycle.

- Admit only an attempt whose stored status is `failed` and whose controlled
  error code is exactly `gmail_rate_limit_rejected`,
  `gmail_pre_send_unavailable`, or
  `credential_or_authorization_rejected`.
- Preserve the campaign's exact next step, including a step 2 or step 3 retry.
  Do not clear or recompute `restaurants.is_contacted`, `email_sent`,
  `email_send_count`, `last_email_sent_at`, `last_email_send_sequence`, or
  `last_email_recipient`.
- Schedule an admitted retry no earlier than the start of the next local
  calendar day's saved outreach window in `Australia/Sydney`, while preserving
  any later campaign hold already applied. Construct the next Sydney date before
  converting it to UTC so the saved wall-clock start follows daylight saving
  rather than behaving like a fixed 24-hour delay.
- Count every non-skipped immutable provider-boundary attempt already created
  for that campaign step. Allow at most three total attempts: the initial
  attempt plus two retries. Existing skipped/redirected safety attempts remain
  outside this failure cap. If the third counted attempt receives another
  allowlisted rejection, stop the campaign with `delivery_retry_exhausted`,
  retain its exact step/timestamp for audit, and require operator review. The
  stopped status keeps it out of eligibility.
- Keep every prior attempt and event immutable. Each retry creates a new attempt,
  reserves a fresh normal mailbox quota slot, and gets a new send sequence. Never
  decrement or reset `usage_count`, `cycle_number`, ramp state, or a restaurant's
  confirmed-send counters after a failure.
- Record whether the failure scheduled another attempt or exhausted the limit so
  the decision can be monitored independently of the provider error. A failed
  code outside the allowlist stops with `delivery_failure_not_retryable`; it does
  not silently become due again. List and claim gates also pause an older
  non-allowlisted failed row if its campaign was manually reopened, including an
  accepted-then-bounced attempt.
- Refuse retry when the same campaign step retains any `sent`, `sending`, or
  `unknown` outcome. When discovered while finalizing a new failed attempt, stop
  with `delivery_outcome_conflict`. A pre-existing manually reopened
  inconsistent row is instead excluded by list and claim gates and appears
  paused until operator reconciliation; the read/claim guard does not rewrite
  history.

The disposition matrix is:

| Stored outcome | Automatic disposition |
| --- | --- |
| `failed` + `gmail_rate_limit_rejected` | Preserve the exact step, cool the selected mailbox until the next Sydney window, and schedule a capped retry no earlier than that window. |
| `failed` + `gmail_pre_send_unavailable` | The OAuth token/pre-send stage failed before `/messages/send`; preserve the exact step and schedule the same capped retry. |
| `failed` + `credential_or_authorization_rejected` | Preserve the exact step, quarantine that mailbox, and schedule a capped retry no earlier than that window that may use another healthy mailbox. |
| Any other `failed` code | Stop for operator review; do not retry automatically. |
| `unknown` / `send_unknown` | Preserve for provider reconciliation; never retry automatically. |
| `sending` past its lease | Reconcile to `unknown`; never assume rejection. |
| `sent` | Advance normally and never retry automatically. |
| `skipped` | Do not admit it to this definitive-failure scheduler or its cap; established skip handling remains separate. |

A Gmail 429 response, or a 403 carrying the recognized
`userRateLimitExceeded`, `rateLimitExceeded`, or `dailyLimitExceeded` reason,
maps to `gmail_rate_limit_rejected`. It cools the quota account through the same
next-window boundary but does not create a credential-health quarantine. A 401,
or a non-rate-limit 403, maps to
`credential_or_authorization_rejected`; the existing dedicated health
quarantine excludes that account until a successful health check or an approved
credential/From-address recovery action clears it.

A transient OAuth token HTTP, transport, read, or malformed-success failure maps
to `gmail_pre_send_unavailable` only because `/messages/send` has not been
called. Known permanent OAuth configuration and authorization failures remain
`credential_or_authorization_rejected`. Cancellation before the message
endpoint is called is also proven non-delivery; cancellation or transport loss
at the message endpoint remains ambiguous.

Requeueing only makes the campaign step eligible at a future time. The persisted
`email_job` control remains authoritative and is never enabled by recovery.
Claim-time lifecycle, interest, consent, sequence approval, enabled-step,
schedule, pacing, quota, account-health, and idempotency checks still fail
closed. In particular, this policy cannot bypass the temporary step 2/3 pause.

The same failure transaction resets only the persisted attempt count on the
exact running `outreach.bulk_send` job linked to the attempt. It preserves that
job's running status, owner, lease, payload, and availability, so the live
heartbeat and normal fenced deferral continue unchanged. If the process crashes
first, lease expiry can reclaim the job despite its normal one-attempt worker
limit. This narrow crash-recovery fence never makes an ambiguous delivery
retryable.

Accepted-then-bounced delivery is outside this automatic path. A future bounce
reconciler must ingest an authenticated, structured DSN or authoritative
provider event and correlate it unambiguously to one immutable attempt using
provider/RFC identifiers. It must fail closed when correlation is missing or
ambiguous and must check for later campaign progress, attempts, or replies before
any compensation. Free-form message text, absence from a mailbox view, and
operator inference are not sufficient. The accepted attempt must remain in the
ledger even if a later reconciliation adds a bounce outcome.

## Options Considered

- Reset restaurant email fields each day: rejected because those fields record
  confirmed historical contact and do not control eligibility for the exact
  failed campaign step.
- Refund or reset mailbox counters: rejected because a provider-boundary claim
  consumed real capacity and a restart must not recreate quota.
- Retry every failed or unknown attempt: rejected because ambiguous failures may
  already have been accepted and would create duplicate outreach.
- Retry immediately in the same window: rejected because it can repeatedly hit
  the same mailbox/provider condition and consume the day's quota.
- Never retry any provider rejection: rejected because explicit pre-acceptance
  rate-limit and credential failures can be recovered safely with bounded delay.
- Infer bounces from Gmail Sent visibility or message text: rejected because the
  signal is incomplete and is not an authenticated, exactly correlated delivery
  disposition.

## Consequences

- A safe rejection returns the exact campaign step to the normal queue no
  earlier than the next Sydney send day, without shortening a later hold or
  erasing restaurant, account, or attempt history.
- Daily totals remain conservative: every failed try consumes a slot, so fewer
  messages may be accepted on a day with provider problems.
- Rate-limit cooldown protects the affected mailbox without falsely marking its
  credentials bad. Credential rejection remains a separate quarantine and may
  route the later campaign attempt through another healthy mailbox.
- Unknown, accepted, and non-allowlisted failures can require manual
  reconciliation; skipped outcomes remain outside this decision. Duplicate
  prevention takes precedence over queue volume.
- The deployed implementation uses the existing campaign, attempt, event,
  schedule, quota, and health schema. Release `82f366f` added no migration and
  left production on schema 54 with `email_job` disabled. A deployment does not
  authorize or enable a real outreach send.

## Deployment and Monitoring

Keep `email_job` disabled while deploying the matching API and worker build.
Run the targeted outreach/provider tests and read-only checks first; do not use a
live provider as a smoke test. The opt-in PostgreSQL transaction suite uses
`TUVI_OUTREACH_TEST_DATABASE_URL` and refuses a database whose name does not
start with `tuvi_retry_test`; point it only at a disposable database migrated to
the current schema. Confirm that production remains on its current schema
because this decision introduces no migration, then verify:

- each allowlisted rejection has one immutable failed attempt and a separate
  retry-scheduled or retry-exhausted audit event;
- the retry lower bound is the next saved Sydney window start, including across
  a daylight-saving boundary, and any later campaign hold remains later;
- a rate-limited mailbox has a quota cooldown but no credential quarantine;
- a credential-rejected mailbox has the dedicated health quarantine;
- no restaurant confirmed-send field or mailbox usage/ramp counter was reduced;
  and
- `unknown`, `sent`, `sending`, and non-allowlisted failures did not become
  automatic retry candidates, and `skipped` did not enter the new
  definitive-failure scheduler or cap.

Use the internal-admin Send history screen for per-Sydney-day, per-sender attempt
details. Monitor only redacted aggregate database counts for error-code and retry
event trends. Enabling production outreach remains a separate human approval.

## Rollback / Revisit Trigger

Disable `email_job` before rolling back the API and worker together. No schema
rollback is needed. Preserve all attempts, events, cooldowns, quarantines, and
campaign timestamps; never mass-reset restaurants or mailbox counters. Before a
prior worker is re-enabled, review campaigns with `retry_scheduled`,
`delivery_retry_exhausted`, or `delivery_failure_not_retryable` history because
the prior binary does not enforce this bounded policy.

Revisit the allowlist, delay, or three-attempt cap only with provider evidence
and an approved sender-reputation plan. Revisit automatic accepted-then-bounced
recovery only when a structured, authenticated, exactly correlated delivery
signal and regression coverage are available.
