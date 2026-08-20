Production remains on release `8c34503` at schema 54; Phase 2/3 automated outreach remains temporarily paused.
Unreleased work applies the active approved signature, adds the internal-admin daily Send history, and schedules bounded next-Sydney-window recovery for definitive pre-acceptance outreach failures without resetting restaurant or mailbox history.
Only controlled Gmail rate-limit, pre-send token, and credential failures retry the exact step; the cap is three attempts, later holds remain intact, and unknown, accepted, or non-allowlisted/bounced outcomes stay paused.
Backend tests (668), targeted race tests (216), four real-PostgreSQL transaction cases, vet/build, OpenAPI, repository context, independent review, and diff checks pass.
No deployment, migration, production mutation, provider call, or real email was performed; API/worker rollout still requires explicit approval.
