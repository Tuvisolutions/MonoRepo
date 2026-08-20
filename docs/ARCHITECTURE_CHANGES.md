# Crucial Architecture Changes

Date: 2026-08-14

This is the short current contract for restaurant lead outreach, restaurant
media, and the public digital-footprint review.

| Area | Current contract |
| --- | --- |
| Lead ingestion | Google Places remains the discovery source. Apollo runs afterward only as optional enrichment when owner or work-email details are missing. Missing Apollo configuration, no-match, and provider failures never discard or block a verified Places lead. Failed durable jobs resume from saved cell/candidate checkpoints; completed cells and imported candidates remain intact. Imports persist a nonempty inferred-business source record and enroll only restaurants with a name and valid business email. |
| Outreach eligibility | `lead` and `emailed` restaurants with recorded `inferred_business` evidence are eligible. Expressed interest pauses automation. Lost, archived, onboarding, and active-client restaurants are excluded. OCR, profile approval, demo publication, legacy campaign readiness, and the legacy suppression table are not gates. |
| Email content | Outreach is a versioned, administrator-approved, plain-text sequence stored in PostgreSQL. It supports adding, removing, disabling, and reordering steps. The renderer substitutes the general greeting, deterministic first-email greeting, restaurant-name, and Tuvi-website tags, then sends the saved body without appending unsubscribe copy. `{{greeting01}}` is allowed exactly once in the first enabled body and never in subjects or follow-ups. |
| Addressing | The renderer greets a safely cleaned known owner by first name. If owner details are absent, it uses `Hi {restaurant name} team,`. The first-email greeting uses only verified Google listing city/cuisine/rating/review-count facts, applies fixed thresholds and fallbacks, and never calls an AI provider. |
| Sequence progress | Each enrollment stores integer `current_step` and `next_step`, plus last-send and next-send timestamps. Only confirmed provider acceptance advances a step. Failed, skipped, or ambiguous delivery does not advance it. The next enabled step defaults to a 72-hour delay. |
| Send ordering | Any unfinished recipient follow-up phase blocks first messages to new recipients, including while the follow-up is waiting for its due time. This completes the existing list before starting new restaurants. |
| Runtime control | A persisted admin switch is the authoritative outreach gate. Disabling it cancels deferred work and prevents another provider boundary; enabling it creates or safely resumes one fenced bulk workflow. Deployment verification never enables it or sends to real leads. |
| Sequence versions | Editing creates a draft version. Approval archives the previous active version, moves only untouched enrollments to the new version, and leaves in-progress recipients pinned to that version's immutable subject/body copy. Scheduled sends and inbox replies resolve the sender signature from the current active approved version at dispatch time, so saved contact details apply to the next outbound outreach message without rewriting historical copy. Selected-version previews and test sends continue to show that selected version's persisted signature. |
| Unsubscribe | Any unsubscribe text or URL is authored in the saved database template. Application code does not generate or validate an unsubscribe merge tag, create unsubscribe tracking tokens, expose an unsubscribe endpoint, write suppressions, or gate delivery on the legacy suppression table. Historical schema and event values remain readable for migration/audit compatibility. |
| Admin portal | The outreach page provides sequence draft/version editing, add/remove/reorder/enable controls, delays, preview and approval, recipient progress, sender health, and the persisted email-job switch. Preview and test-send can search existing restaurants, render authoritative facts server-side, and show the non-sensitive fact categories used. Restaurant media is approved or rejected manually. |
| OCR | OCR workers, cron wrappers, provider code, image-classification jobs, configuration, and provider credentials are retired. Historical database columns and old migrations remain temporarily for audit and rollback compatibility but have no runtime role. |
| Restaurant media | Scrapers persist text/menu facts without third-party image URLs or bytes. Public Google photos are resolved live with attribution and are not stored. Durable public media must be owner-granted or licensed and manually approved. Legacy scraped images fail closed on public API, report, and template boundaries. |
| Public AI review | The digital-footprint report runs independent sources concurrently under a 15-second server budget. Same-place requests coalesce, global report/browser work is bounded, and slow providers produce a clearly labeled conservative partial result. Chromium runs as an unprivileged sandboxed user behind DNS-rebinding-resistant public-network enforcement. |
| Mobile report | Restaurant identity, score/status, live attributed photos, and map are placed near the top of the mobile report instead of being hidden below long analysis sections. |
| AI provider | The API reads the vision-capable model and key only from protected host configuration. The production rollout reuses the existing protected OpenAI key in place; no key is copied into source or logs. |

Operational details and rollback checks are in
[lead-scrape-outreach.md](runbooks/lead-scrape-outreach.md) and
[vm-deployment-plan.md](runbooks/vm-deployment-plan.md).
