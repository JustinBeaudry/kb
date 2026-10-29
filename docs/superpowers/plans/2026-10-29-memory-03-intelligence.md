# Verified Memory and Dreaming Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish supported project knowledge and qualified shared lessons, with Buzz decisions and an evidence-grounded conversational agent.

**Architecture:** Verification and publication are distinct service operations. Background jobs consume immutable evidence revisions; LLM output remains candidate material. Owner decisions bind exact revisions and evidence; Hermes uses the ordinary API.

**Tech Stack:** Go services and graph repositories, one LLM gateway, Buzz CLI gateway with locally verified signatures, thin Hermes plugin where its qualified version supports it.

**Spec:** [Approved design](../specs/2026-10-29-persistent-memory-design.md); [shared contracts](2026-10-29-persistent-memory.md).

## Global Constraints

Shared constraints apply verbatim. No test result or owner decision from self-labeled agent payloads. No model-only causal verification. No provider fallback outside policy. Cross-project lessons need independent publication/sharing checks. Missing cost/usage is unknown, never zero.

## Review Focus

- An assistant repeats its earlier claim: no new independent corroboration (I1/I3).
- Authentic evidence supports only a narrower claim: preserve conditions and reject overgeneralization (I1).
- Signed but stale/wrong-owner Buzz reply: reject without acting (I2).
- Model times out after reservation: bounded spend accounting and safe retry (I3).
- Hermes built-in notes conflict with graph: notes cannot override verified authority (I4).

### Task I1: Evidence verification and atomic publication

**Files:** Create `server/internal/service/{verification.service.go,verification.service_test.go,publication.service.go,publication.service_test.go}`, `server/internal/repository/{verification.repository.go,publication.repository.go,publication_integration_test.go}`, `server/internal/gateway/{git.gateway.go,git.gateway_test.go}`, `server/internal/domain/verification_policy.go`, `server/migrations/006_verification.cypher`.

**Interfaces:** `VerificationService.Check(ctx,p Principal,revisionID string) (Verification,error)`; `PublicationService.Publish(ctx,p Principal,revisionID,expectedHead,verificationID string) (Publication,error)`; `PublishLesson(ctx,p Principal,input LessonInput) (Publication,error)`; LessonInput includes text, applicability, support revision IDs, requested sharing scope and expected head. Only authenticated verifier capability can invoke these internally. `SourceReader.ReadSnapshot(ctx,repoID,revision,path string) (SourceSnapshot,error)` returns bytes, digest, locator, observed revision and gateway Origin. Use an approved repository mapping, never arbitrary URLs/paths. Repos accept Tx and enforce F1 lock protocol.

- [ ] Write `TestUnattestedTestOutputCannotVerify`, `TestAuthenticatedPreferenceRequiresExactStatement`, `TestVersionRangeIsNotInstalledVersion`, `TestFailureDoesNotProveCause`, `TestRepeatedDerivedSourceCountsOnce`, `TestWrongEnvironmentCannotPublish`. Assert supported narrow observations only; non-supported outcomes remain candidate with reason codes. Real-graph race test: publication vs evidence invalidation yields no eligible stale publication.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```go
assert.Equal(t, "unsupported", string(verification.Outcome)) // unattested test result
assert.Empty(t, eligiblePublications) // invalidation race
assert.Len(t, distinctEvidenceOrigins, 1) // repeated source
```

- [ ] Run `go test -race ./internal/service ./internal/repository ./internal/gateway -run 'Test(Unattested|AuthenticatedPreference|VersionRange|FailureDoes|RepeatedDerived|WrongEnvironment|PublicationRace)' -v`; confirm red.
- [ ] Implement versioned deterministic evidence policies. Authenticate observed source and provenance, then validate claim semantics and applicability. Initially automatically verify explicit owner preferences, exact declared versions/contracts, and genuinely attested execution results; arbitrary causal explanations require owner adjudication. Check current source heads, revisions and transitive support under lock; atomically publish and enqueue index/digest jobs. Shared lessons must remove restricted source content and preserve qualified applicability; any unresolved sensitivity stays pending. No arbitrary verifier command execution.
- [ ] Run policy fixtures and real graph races; assert ordinary clients cannot invoke publication, and stale-index retrieval from F5 excludes invalidated support. Use legitimate fixtures for positive verified retrieval. Run Go gate.
- [ ] Commit as `feat: verify evidence before publishing shared memory`.

### Task I2: Buzz decisions and owner recovery

**Files:** Create `server/internal/service/{decision.service.go,decision.service_test.go}`, `server/internal/repository/{decision.repository.go,decision_integration_test.go}`, `server/internal/gateway/{buzz.gateway.go,buzz.gateway_test.go,nostr.go,nostr_test.go}`, `server/internal/handler/decision.handler.go`, `server/internal/domain/owner_challenge.go`, `docs/runbooks/buzz.md`; extend `server/cmd/kb-admin/main.go`, `server/migrations/007_decisions.cypher`.

**Interfaces:** `DecisionService.Request(ctx,p Principal,d Decision) (Decision,error)`; `Respond(ctx,reply DecisionReply) (Decision,error)`; `CreateOwnerChallenge(ctx,requestedAction,digest string) (OwnerChallenge,error)`; OwnerChallenge has ID, nonce, expiry and action digest, no bearer. `BuzzGateway.Send(ctx,operationKey,threadID,body string) (MessageReceipt,error)`; `Poll(ctx,cursor string) ([]SignedMessage,string,error)`. Normalize vendor events before services; verifier checks signature/event identity locally. Recovery challenge grants a one-use scoped action token only after enrolled-owner signed approval.

- [ ] Write table `TestDecisionReplyValidation`: wrong key, bad signature, wrong channel/thread, expiry, changed revision/evidence, duplicate event and replay all reject or return exact prior receipt without new effect. Write `TestBuzzOutagePreservesPendingDecision`, `TestOwnerChallengeCannotElevateAgentToken`, `TestAckLostDoesNotDuplicatePost` using gateway-boundary fakes and qualified relay fixture.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```go
assert.ErrorIs(t, err, domain.ErrStale) // changed candidate revision
assert.Equal(t, "pending", string(decision.State)) // Buzz outage
assert.Equal(t, 1, deliveredMessages) // lost acknowledgment replay
```

- [ ] Run `go test -race ./internal/service ./internal/gateway -run 'Test(Decision|Buzz|OwnerChallenge|AckLost)' -v`; confirm red.
- [ ] Implement using Software Factory gateway/nostr patterns after inspecting current versions and attribution requirements. KB uses separate credentials, allowlisted subprocess environment, stdin message body, secret-redacted errors, bounded output/time, and no secret argv. Reconcile ambiguous send failures using operation identity before retry. Record owner judgment separately from empirical verification; decisions expire in 24h by default. All mutations recheck evidence revisions in the publication transaction.
- [ ] Run signed synthetic round trip, stale-decision race, duplicate delivery and outage tests. Actual private Buzz post/reply acceptance runs only against configured KB identity/channel in O3. Recovery flow must work without giving owner tokens to the agent process.
- [ ] Commit as `feat: adjudicate memory decisions through Buzz`.

### Task I3: Budgeted dreaming and actionable findings

**Files:** Create `server/internal/service/{dreaming.service.go,dreaming.service_test.go,model_budget.service.go,notification.service.go}`, `server/internal/gateway/{llm.gateway.go,llm.gateway_test.go}`, `server/internal/repository/{model_budget.repository.go,findings.repository.go}`, `server/internal/domain/{generation.go,finding.go}`, `server/migrations/008_dreaming.cypher`, `docs/runbooks/dreaming.md`; extend worker registration and status API.

**Interfaces:** `DreamingService.Process(ctx,job Job,lease Lease) error`; `ModelBudgetService.Reserve(ctx,scope Scope,route string,maxCostMicros int64) (Reservation,error)`; `Settle(ctx,reservationID string,usage Usage) error`; `LLMGateway.Generate(ctx,request GenerationRequest) (Generation,error)`. Domain request includes route/model/schema/prompt excerpts/idempotency key/deadline; Generation includes validated candidate proposals and Usage with explicit Known flags, token counts/cost. `NotificationService.Dispatch(ctx,job Job,lease Lease) error`; Finding contains source revisions, relevance rationale, applicability, status and stable digest.

- [ ] Write `TestDreamCannotVerifyOwnConclusion`, `TestSupportChangesBeforeDreamCommit`, `TestUnknownUsageDoesNotBecomeZero`, `TestConcurrentReservationsStayWithinBudget`, `TestNoAuthorizedRouteLeavesJobPending`, `TestNoChangeFindingSendsNoNotice`, `TestSensitiveFindingDoesNotReachBuzz`. Provider fixture returns malicious instructions, malformed JSON, unsupported facts, and valid narrow observations; only validated candidates emerge.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```go
assert.Equal(t, "candidate", string(proposal.Status))
assert.False(t, settledUsage.CostKnown)
assert.Equal(t, reservedMicros, stillReservedMicros)
assert.Empty(t, sentNotices) // unchanged finding
```

- [ ] Run `go test -race ./internal/service ./internal/gateway -run 'Test(Dream|SupportChanges|UnknownUsage|ConcurrentReservations|NoAuthorized|NoChange|SensitiveFinding)' -v`; confirm red.
- [ ] Implement incremental jobs on new events, task boundaries/compaction and hourly scheduler scan; unchanged input digests do no work. Set 120-second generation deadline, three bounded model attempts, 16 KiB excerpt input per invocation plus fixed instructions, and route-specific max output tokens. Reserve worst-case configured cost before calls; absent pricing/budget leaves model jobs pending. Unknown settlement retains the reservation until reconciled; never count zero. Require owner-configured daily cap; no invented spend permission. One gateway instruments every generation. Publish only through I1. Build daily digest, immediate active-work findings and pending decisions via I2 outbox; recheck relevance/current state before sending.
- [ ] Run fake-provider failures, lease-loss/cancellation, repeated cycles and ledger races. Require no duplicates, bounded context/spend and no direct verified writes. Verify retrieval/capture remain available when model budgets are exhausted. Run Go gate.
- [ ] Commit as `feat: consolidate memory with budgeted background dreaming`.

### Task I4: Hermes conversation over authoritative memory

**Files:** Create `integrations/hermes/{pyproject.toml,uv.lock,README.md}`, `integrations/hermes/src/kb_memory/{provider.py,gateway.py,models.py}`, `integrations/hermes/tests/test_provider.py`, `docs/integrations/hermes.md`, `server/internal/acceptance/conversation_test.go`; modify qualification matrix from C2.

**Interfaces:** Internal Python `MemoryGateway.orient(scope: Scope, query: str, budget_bytes: int) -> MemoryResult`, `fetch(record_id: str, locator: str, budget_bytes: int) -> MemoryResult`, `submit(events: list[Event]) -> Receipt`. Dataclasses internal, Pydantic at HTTP/plugin boundaries. Exact Hermes hook methods are copied from the pinned qualified plugin API, not invented in this plan. If native provider cannot implement a required hook, use C3 MCP tools and mark automatic hook unsupported; do not pretend parity. Provider exposes only candidate write and retrieval credentials.

- [ ] Write `test_answers_preserve_evidence_references`, `test_assistant_echo_is_not_independent_evidence`, `test_builtin_note_cannot_override_graph`, `test_forbidden_source_is_not_retried_with_broader_scope`, `test_background_prefetch_respects_budget`. Assert returned claim/evidence revision IDs and no promotion call from plugin; test route through real API with synthetic data.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```python
assert result.evidence_revision_ids == expected_revision_ids
assert gateway.publication_calls == []
assert result.bytes <= requested_budget
```

- [ ] Run `uv run pytest tests/test_provider.py` in plugin directory; confirm expected failures after pinned Hermes interface compatibility is established. No package installation during planning.
- [ ] Implement a thin provider and setup documentation. Route conversation captures through C1-equivalent filtering with shared fixture vectors; never double-upload through both generic collector and provider (one elected capture owner per session). Attach graph citations to answers, present uncertain connections explicitly, and let owner discuss dream findings. Runtime's local notes remain working state. Do not add shell/engineering tools solely to support knowledge conversation.
- [ ] Run `uv run ruff check . && uv run mypy . && uv run pytest`, plus Go conversation contract. Qualify actual answer grounding in O3; passing plugin tests alone is not conversational acceptance.
- [ ] Commit as `feat: connect Hermes conversations to shared graph memory`.
