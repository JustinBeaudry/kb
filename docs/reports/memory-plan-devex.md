# Private memory plan: developer experience preflight

Date: 2026-10-29
Branch reviewed: `docs/persistent-memory-design`
Reviewed starting commit: `7c001d1`
Status: **DONE_WITH_CONCERNS**, with bounded implementation rulings below.
Mode: **DX POLISH**. Product: private API, CLI, MCP, and harness integrations.

This review applies `/plan-devex-review` to Justin and his agents. The owner has
approved execution. Persona, scope, and reversible interface decisions therefore
do not require another approval conversation. This report is the only file changed
by this reviewer. The controller owns incorporation into plans and code.

## Target developer and perspective

| Field | Evidence-backed persona |
| --- | --- |
| Who | Justin, operating his private service; agent clients acting on individual projects/tasks |
| Context | Resume engineering work, find applicable contracts and decisions, avoid repeating a failed attempt |
| Tolerance | Will configure infrastructure once; recurring retrieval should require one explicit command and no owner intervention |
| Expects | Preserved local vault, bounded cited output, useful failure reasons, no surprise upload or model charge |
| Authority | Justin administers; agents retrieve/submit within granted scopes and cannot promote themselves |

Evidence: design lines 20-37, 61-78, 184-191, and 292-314; shared plan's global
constraints. This is not public SaaS acquisition. Pricing pages, free tiers,
community growth, browser playgrounds, and organization administration add no value
to this release.

**Developer perspective, inferred from current files:**

I already have a Markdown vault. I open the README and see “Install and initialize,”
then `kb init`, `kb map`, and `kb get-node`. That is a familiar path. I want my next
agent to remember the relevant work, but I do not want installing a new binary to
start uploading old sessions. The plan preserves this, so I should be able to keep
using the local commands while adding a separate remote profile.

I now need to connect a client. The plan says my token belongs in OS credential
storage, but it does not yet show the operation that creates a profile or how the
one-time client credential reaches that store without entering an agent transcript.
I need an operator setup path first, with Buzz recovery explained separately. I
should not have to debug a missing recovery feature to enroll the first client.

Once connected, an empty shared search is plausible: my first upload is a candidate.
I need the example to show that candidate in an explicitly scoped working view,
then show an independently verified claim later. A successful upload must not look
like failed retrieval, and a successful HTTP response must not imply verification.
When the service is offline, I need to see that fact and the capture queue state,
continue my foreground task, and know which safe command to run next.

## Existing patterns to preserve

- `README.md:10-51` supports a standalone binary and Bun source invocation, with
  explicit local vault selection. `src/cli.ts:11-27` registers lazy citty commands.
- `README.md:109-141`, `src/lib/envelope.ts:27-51`, and
  `src/lib/retrieval.ts:21-68` establish schema-v2, strict explicit budget flags,
  complete-wire accounting, diagnostic stderr, and structured continuation.
- `skills/kb/references/query.md:9-29` teaches navigation before evidence;
  lines 50-73 explain safe continuation and intact wire parsing; lines 75-89
  distinguish no results, uncertainty, and read-only querying.
- `README.md:188-210` separates evidence from instructions, gated source reads,
  and optional Bun-dependent native hooks. New remote integration must not inherit
  local `KB_APPROVE` as remote administration authority.
- `package.json` is v0.7.0, with existing Bun lint/test commands. No current hosted
  API, remote CLI, MCP launcher, or changelog was found in the reviewed checkout.
- The four linked plans already require idempotency, revocation, offline recovery,
  redaction, generation cleanliness, restore drills, and private acceptance. Reuse
  these tests rather than add a second implementation of their policies.

## Relevant comparison and first useful result

These are workflow comparisons, not externally measured competitor claims. No
network research or external action was performed in this scoped preflight.

| Workflow | Current evidence | Time evidence | Implication |
| --- | --- | --- | --- |
| Existing local KB | README install/init, then map/get-node | Not measured here | Keep it available without remote credentials |
| Ordinary file search | O3 explicitly retains this capable baseline | No trial performed | Remote memory must earn its added setup cost |
| Proposed remote KB | F2 enrollment, C3 profile, F4/F5 evidence and retrieval | Not executable yet | Measure client onboarding separately from server provisioning |

**Target, not a result:** after service provisioning and credential enrollment,
profile setup plus the first cited synthetic result should take <=5 minutes in one
terminal session. One-time graph/provider deployment is a separate operator runbook,
not hidden inside that timing. Current TTHW cannot be estimated credibly from the
unimplemented commands. Do not label it champion/competitive until timed.

The useful first moment is a second client resuming a synthetic task and retrieving
the exact objective, pending step, and cited narrow finding. Deliver it as a small
copy-paste terminal walkthrough, not another hosted product. At foundation stage,
show the finding as `candidate` in task working mode. After I1, show the independently
verified counterpart and its evidence revision. No forged production verification.

## Findings and implementation rulings

### 1. P1: resolve MCP transport and missing status dependency before C3

**Evidence:** capture plan lines 69-84 names `mcp.handler.go`, defines
`NewMCPHandler(...) http.Handler`, lists `memory_status`, then promises stdio first.
Its constructor supplies no status reader. An HTTP handler is not a stdio launcher.

**Ruling:** use a shared MCP protocol dispatcher with retrieval, candidate,
checkpoint, and status consumer interfaces, plus an explicit stdio composition root
and documented launch command. Standard output contains protocol frames only;
diagnostics go to stderr. The dispatcher invokes the same application services as
HTTP and derives the principal from the selected restricted client profile. It
never accepts a client-supplied principal or owner capability. HTTP MCP is exposed
only after its separate authentication qualification. Controller confirmed this
correction during preflight.

**Acceptance:** start the real stdio process with a synthetic restricted profile,
initialize, call status/orient, verify budgeted framing and clean shutdown. An
owner-admin tool is absent; missing credentials produce a protocol error without
stdout noise. Ownership: C3, with F1 contract alignment.

### 2. P1: make operator bootstrap usable before Buzz recovery exists

**Evidence:** foundation lines 49-62 specifies one-time credentials and local owner
setup but defers recovery to I2. Intelligence lines 48-64 specifies short-lived
Buzz-confirmed challenges. Capture lines 15 and 84 prohibits secret argv/output and
requires OS-held profile credentials, but gives no transfer sequence.

**Ruling:** document two distinct paths. F2 provides a one-time, operator-only setup
on the controlled service host that enrolls the owner public identity and provisions
the first project-scoped client. Reject setup after initialization. I2 adds signed
Buzz recovery; before I2, report `recovery_unavailable` and preserve fail-closed
behavior. The initial setup does not depend on a working Buzz relay. No reusable
owner token is given to model tools.

Use `kb remote profile add <name> --url <url>` as the client profile operation, with
an interactive hidden credential entry or protected stdin transfer, not `--token`
or a token printed in a walkthrough. Enrollment and profile import are separate:
profile creation cannot mint authority. Pin endpoint identity in the profile,
require TLS except an explicitly selected loopback fixture, and never forward a
bearer across an origin-changing redirect. Missing OS credential access fails with
a repair instruction. No implicit plaintext fallback.

**Acceptance:** F2/C3 show setup -> scoped enrollment -> OS credential import ->
status with no secret in argv, stdout, logs, or saved example; second setup is
refused; missing Buzz does not block normal client retrieval. Recovery tests remain
in I2. Ownership: F2, C1/C3, I2, onboarding runbook.

### 3. P1: freeze one wire contract before generating clients

**Evidence:** shared plan defines domain `RetrievalResult` and `EvidenceChunk.Trust`
but leaves Trust a free string, and separately requires explicit verification
status. C3 introduces a separate validated remote envelope; operations line 99 uses
`returnedClaim.Trust == "verified"`. The design distinguishes claim status,
verification outcome, authenticated origin, and publication. Collapsing these into
one string loses that distinction.

**Ruling:** F1 OpenAPI must define closed enums and separate fields for claim status,
origin trust, and verification outcome when applicable; use the same meaning across
HTTP, CLI, MCP, and Hermes. Keep verification outcome optional where no check exists,
never manufacture `supported`. Define exact JSON naming, required/optional fields,
`schema_version: "3"`, source revision/locator, warnings, no-results, truncation,
and cursor fields in one sample contract. HTTP uses its JSON DTO; CLI uses a separate
length-prefixed schema-v3 rendering; MCP uses protocol framing. Each renderer counts
its complete output. Keep `src/lib/envelope.ts` schema-v2 parsing unchanged.

**Acceptance:** table-driven contract vectors cross all renderers, covering one
candidate, one verified record, conflict, no-results, unavailable source, and tiny
budget. Assert statuses and evidence references, not just a matching version string.
Correct the O4 trust assertion to check the actual explicit claim-status field.
Ownership: F1/F5, C3, I4, O4.

### 4. P2: make scope, working mode, and continuation discoverable

**Evidence:** design lines 230-259 requires registered project scope and task-only
candidate visibility. Foundation lines 124-125 correctly keeps verification out of
F5 and calls for upload -> candidate -> task-only retrieval. Capture lines 71-85
lists operations without specifying CLI flags or continuation execution.

**Ruling:** the remote command family uses `--profile`, `--project`, `--task`,
`--session`, `--budget`, and opaque `--continuation` consistently. Default retrieval
is verified shared memory; `--mode working` requires an explicit task and permits
only that task's authorized working state. Do not infer remote scope from local
vault names. When multiple projects are authorized and no explicit binding exists,
return a compact scope-required error rather than search everything.

The first-result walkthrough includes:

```sh
kb remote status --profile fixture
kb remote orient --profile fixture --project demo --task resume-demo --mode working --budget 4096
kb remote search "retry contract" --profile fixture --project demo --budget 16384
```

These are selected target command shapes, not commands claimed to work today.
The fixture and its write/setup steps must ship with the walkthrough. Structured
continuation identifies the remote command, arguments, profile, scope, and budget;
the opaque server cursor remains bound to principal and epochs. Execute argument
arrays without shell interpolation. A stale cursor fails with a safe restart
instruction, never silently restarts against a different snapshot or local vault.

**Acceptance:** command help shows these flags and a small working-mode example;
tests cover ambiguous scope, cross-task denial, Unicode framing, and continuation
after revocation. Ownership: F5/C3 and query guide.

### 5. P2: specify actionable errors and operational status

**Evidence:** shared plan enumerates sentinel errors without final problem fields;
C3 RemoteStatus lists only connection/projects/capability gaps. C1 and O1 already
require pause, overflow/loss, and asynchronous purge status. I3 already requires
pending reasons when model routing/budget is unavailable.

Use a consistent safe problem shape: stable `code`, short `message`, safe `action`,
`request_id`, and a relevant runbook section; optional retry delay and budget
minimum only where safe. CLI failure uses nonzero exit, stderr diagnostics, and no
success envelope; MCP uses protocol errors; HTTP uses documented non-2xx problems.
Do not expose paths, object keys, token material, or the existence of forbidden IDs.
Do not distinguish unknown and forbidden record existence in public diagnostics.

| Error trace | Current specification | Required user-visible remedy |
| --- | --- | --- |
| Absent/expired/revoked profile credential | C3 requires a failure test | Credential unavailable; reconnect this profile through owner enrollment; no local fallback and no secret dump |
| Stale continuation after policy/deletion change | F5 requires rejection | Results changed; rerun the original scoped retrieval, without an automatic broader retry |
| Capture offline or spool quota reached | C1 retries and records gaps | Capture queue/degradation and known loss count remain visible; reconnect or resolve quota; foreground work continues |

Status should expose authorized metadata only: endpoint/profile and protocol
compatibility, capture paused/enabled/offline state, queued bytes/event count,
last acknowledged sequence/time, gap count, policy revision/expiry, and capability
limits. Owner inspection can add pending/dead jobs, budget reasons, purge progress,
and backup freshness. Ordinary agent status cannot list other clients or private
projects. Do not leak source text through “verbose” mode. These fields compose the
already planned status/runbooks, not a new dashboard. Ownership: F2/F3, C1/C3, O1/O2.

### 6. P2: document safe upgrades before private rollout

**Evidence:** schema-v2 preservation, pinned versions, graph migrations, backup
manifests, and C4 selected import are planned. No explicit deployed client/server
compatibility policy or upgrade/rollback walkthrough exists yet.

**Ruling:** publish supported API/envelope majors and pinned harness/plugin versions
in status, release notes, and the versions manifest. Reject unsupported remote
majors before interpreting payloads. Test the previous supported client against
the new server, including schema-v2 local behavior, and test interrupted migration
reruns against a restored graph. Do not promise arbitrary old-client support.

The deployment runbook orders preflight/version check, capture spooling, worker
drain, a verified backup, graph migration, server/worker rollout, readiness and cited
retrieval, then client/plugin rollout. Explicitly state whether each migration
permits binary rollback. If not, use the tested restore procedure with deletion
journal replay before readiness; never restore an old snapshot directly into
service. Selected vault import remains a separate preview/manifest operation.
Ownership: F1, C3/C4, O2/O4; no new infrastructure required.

## Journey and first-time confusion check

| Stage | Developer action | Friction identified | Resolution to implement |
| --- | --- | --- | --- |
| Discover | Read README / query guide | Existing README describes only local KB | Add a short explicit remote path beside the preserved local path, C3 |
| Install | Use existing binary/source entry | First enrollment and profile transfer unspecified | Operator setup and secret-safe profile import, finding 2 |
| First result | Retrieve synthetic task state | Ordinary search correctly excludes a first candidate | Explicit task working walkthrough, findings 3-4 |
| Real use | Connect stdio MCP / native adapter | HTTP constructor contradicts stdio; native capabilities vary | Dispatcher/launcher and evidence-backed matrix, finding 1 and existing C2 |
| Debug | Run status after outage | No agreed error/status field contract | Finding 5, preserve foreground progress |
| Upgrade | Update service and adapters | Compatibility and rollback sequence unspecified | Finding 6, tested recovery and pinned matrix |

Inferred roleplay, not timed observation: T+0:00 README gives a clear local install.
T+0:30 remote setup has no executable example yet (finding 2). T+1:00 the first
candidate is absent from shared search by design (finding 4). T+2:00 stdio launch
cannot be derived from the HTTP handler signature (finding 1). T+3:00 no actual
success can be claimed until implementation and the timed walkthrough run. Each
confusion has an assigned task above; none requires a new product decision.

## Eight-pass scorecard

All numbers are **inferred planning assessments, not measured product scores**.
There is no prior live DX baseline or measured trend. “Target” requires implemented
and observed evidence, not merely acceptance of this report.

| Pass | Current plan / 10 | Target / 10 | What closes the gap |
| --- | --- | --- | --- |
| Getting started | 6 | 8 | Secret-safe profile/bootstrap and executable cited fixture walkthrough |
| API / CLI / MCP | 6 | 8 | Consistent DTO/status/cursor semantics and real stdio launcher |
| Errors / debugging | 6 | 8 | Three error traces above plus authorized degradation status |
| Documentation / learning | 7 | 8 | C3 remote/query docs ship with clients; O4 onboarding does not become the first usable instructions |
| Upgrade / migration | 6 | 8 | Explicit compatibility, migration and rollback drill |
| Developer environment | 8 | 8 | F1 local qualified graph; O2 reproducible fixtures; ordinary client needs no graph credentials |
| Owner / agent ecosystem | 8 | 8 | Existing pinned capability matrix, Buzz route, and candid unsupported cells |
| DX measurement | 7 | 8 | Time fresh profile -> cited result and resume journey in C3/O4, separately from O3 usefulness trials |

Overall inferred plan score: **6.8/10**, implementation target **8/10**. TTHW and
competitive rank: **unmeasured**. Zero-friction setup has a gap; bounded defaults,
learn-by-doing, uncertainty handling, and code-in-context have concrete assigned
fixes. The useful resume moment is specified, not delivered.

## Implementation checklist and scope

- [ ] F1 freezes OpenAPI problem/status/evidence enums and representative wire vectors.
- [ ] F2 provides operator setup without waiting for I2 recovery; no owner secret reaches agents.
- [ ] C3 ships executable profile, working retrieval, continuation, and stdio examples with clients.
- [ ] C1/C3 status makes pause, offline queue, loss, and missing policy visible without payload disclosure.
- [ ] O2/O4 demonstrates compatibility/upgrade/restore and records actual timings.
- [ ] `/devex-review` measures help, credential failures, first cited result, and resume after clients work.

Not in scope: public sandbox, marketing funnel, team roles, public community/support
channels, extra SDK languages, auto-updater, editor extension, or codemods for an API
that has not shipped. No TODO file or extra workstream was created.

No owner decision blocks local implementation. F1 storage/tool qualification and
the six contract corrections above precede dependent implementation. Live credentials,
Buzz identity/channel, spending caps, import selection, and actual harness/provider
capabilities remain the already declared acceptance prerequisites. No paid resources,
messages, deployment, benchmark run, or private acceptance was performed here.

Review readiness: this is a DX preflight only. It does not establish engineering,
security, code-review, or release clearance. The scoped task omitted optional outside
voice/subagents, external benchmarking, skill telemetry writes, and plan-file edits;
the controller retains those responsibilities where appropriate.
