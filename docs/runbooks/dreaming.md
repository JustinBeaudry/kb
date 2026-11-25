# Budgeted dreaming

Dreaming is background consolidation. It reads current claim revisions in one project, asks one owner-selected model for narrow observations, and stores what comes back as **candidate** claims plus findings. It never verifies or publishes. Publication still goes only through evidence verification and owner adjudication (I1/I2). Retrieval and durable capture keep working when dreaming is unconfigured, deferred or out of budget.

## Prerequisites (owner-configured, never guessed)

Dreaming spends nothing until all of these are present. Each missing item leaves `dream.consolidate` jobs queued with a visible reason in `GET /v1/status` (owner principal only, `dreaming.reason_codes`).

| Requirement | Configuration | Reason code when absent |
|---|---|---|
| Provider credential | `KB_MODEL_API_KEY` (worker only). `KB_MODEL_ENDPOINT` defaults to `https://api.anthropic.com` | `model_route_unavailable` |
| Route with pricing | `KB_MODEL_ROUTES`: JSON list of `{name, model, max_output_tokens, input_micro_usd_per_mtok, output_micro_usd_per_mtok}`. Prices are integer micro-USD per million tokens. Zero or missing price means unknown | `model_pricing_unavailable` |
| Project authorization | The project's capture policy `AllowedModelRoutes` must name the route. There is no fallback to another provider or route | `model_route_unavailable` |
| Daily cap | `KB_MODEL_DAILY_CAP_MICRO_USD`, an integer greater than 0 (UTC day, all routes) | `model_budget_unconfigured` |
| Remaining budget | Reservations for today below the cap | `model_budget_exhausted` |

`KB_DIGEST_HOUR_UTC` (0–23, default 0) sets when daily digests become due. Look up current model IDs and prices in the provider's official documentation when you configure a route. Never copy them from another project. Supply the key as a sealed variable to the worker only, following [credentials](credentials.md). Malformed values stop the worker at startup (fail closed).

A deferred job does not use up an attempt. It comes back every hour until its prerequisite exists.

## What a cycle does

1. **Scheduling.** The worker sweep (every minute) enqueues a job for each project with newly ingested claim revisions. New events, task boundaries and compaction all show up this way. A full scan runs hourly. Each job is keyed by an input digest over the project's current revision IDs and policy revision. When that digest equals the stored per-project cursor, no job is enqueued and no model call happens.
2. **Bounded input.** The job sends up to 50 current candidate/verified revisions, at most 16 KiB of excerpt text, plus fixed instructions. Excerpts travel as quoted JSON data inside the user message and are never part of the system prompt. Revisions authored by `kb-dreaming` are never sent, so a dream cannot cite its own earlier conclusion.
3. **Reservation.** Before each call, the worst-case cost (input bytes as an upper bound on input tokens, plus the route's `max_output_tokens`, at configured prices) is reserved against today's cap. Reservations are checked and inserted under the OwnerState lock, so concurrent workers cannot exceed the cap together.
4. **Generation.** One call has a 120-second deadline. A job makes at most three bounded attempts. Each attempt makes its own reservation and settles it.
5. **Settlement.** Known usage settles at measured cost, rounded up. Unknown usage leaves the reservation **unreconciled**: it keeps counting at its full worst case. Unknown usage covers a timeout, transport failure, provider error, missing usage, or cache-token billing that the route does not price. Unknown is never recorded as zero.
6. **Validation.** The response must match the JSON schema exactly. A response with unknown fields, such as a `status` field, is rejected as malformed. Each proposal must cite only revisions presented in that call. Proposals with unsupported or invented support are dropped. Status is always `candidate`.
7. **Commit.** Under the live job fence, the job rechecks the policy revision and every cited revision. Each must still be its claim's head, with the same status and text. If anything changed, nothing is written (`ErrStale`), and the job retries against current state. Otherwise the job writes candidate claims (`kb-dreaming` task scope), findings with stable digests, the outbox notices and the cursor.

## Findings and notices

- A finding's digest covers its project, kind, normalized text and sorted support. When a later cycle reaches the same conclusion, it creates no new candidate and sends no new notice.
- Findings are sensitive when the model flags them or their text looks like a credential. Sensitive findings are `withheld`: they stay available on demand and never enter the Buzz outbox.
- A contradiction that involves a verified claim gets an immediate notice. Other findings go into one daily digest per project.
- `dream.notify` jobs (`NotificationService.Dispatch`) recheck each finding right before sending. A finding is dropped if it is not pending, is sensitive, or has support that is no longer current. The notice ID is the operation key, so a retry after a lost acknowledgment can be deduplicated downstream. A notice that was sent is never sent again.
- **Current gap:** the Buzz gateway (I2) is not wired yet. The worker does not claim `dream.notify`, so notices wait in the outbox, and the owner status shows them as pending jobs.

## Operations

- **Status:** `GET /v1/status` as the owner shows `dreaming.state` (`unconfigured`, `available`, `pending`, `budget_exhausted`), queued dream/notify jobs, reason codes, today's cap, committed spend and unreconciled reservations.
- **Reconciling unknown spend:** find the call in the provider's usage report and then call `ModelBudgetService.Reconcile` with measured usage. No admin command exists yet, so treat this as an open item. Until you reconcile, the worst-case hold stays counted.
- **Pausing:** remove the route from the project's `AllowedModelRoutes`, or set the cap to 0. Jobs then defer. Capture and retrieval are unaffected.
- **Telemetry:** the LLM gateway is the only instrumentation point. Each generation emits one `model_generation` span with route, model, policy revision, outcome, `tokens_known`, `cost_known`, and tokens/cost only when known. Prompts, excerpts, outputs and credentials are never recorded.
