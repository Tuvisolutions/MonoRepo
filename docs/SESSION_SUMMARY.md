Production runs release `82f366f` at schema 54; API, worker, and admin are healthy, while `email_job` is disabled and Phase 2/3 remain parked until August 2027.
Outreach → Send history now lists each sending email ID with daily attempts, Email 1/2/3 sent totals, other outcomes, and recipient/subject/provider drill-down.
Scheduled sends and inbox replies use the active saved signature, including its phone/address details, while message copy remains pinned to its approved sequence version.
Definitive Gmail rate-limit, pre-send, and credential failures now retry the exact step at a later Sydney window with a three-attempt cap; unknown and accepted-then-bounced outcomes remain paused.
The rollout created no delivery attempt, outbound message, provider health action, or schema change; re-enabling Phase 1 outreach requires a separate explicit administrator action.
