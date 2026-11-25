// Frozen synthetic corpus and gold for APP-15 (O3 usefulness). Synthetic only.
// One record list feeds both the remote seeding (seed.mjs) and the local vault
// (prepare.mjs), so both memory arms hold the same text with the same trust labels.

export const projects = { atlas: 'atlas-api', birch: 'birch-pipeline' };

// status: verified (owner adjudicated via kb-admin verify) | candidate (submitted, never verified)
// supersedes: claim published as a new revision of an existing target (ExpectedTargetHead).
export const records = [
  // ---- atlas-api ----
  { id: 'atlas-webhook-queue', project: 'atlas-api', title: 'Atlas webhook queue', status: 'verified', revisions: [
    'Decision 2026-10-02: Atlas outbound webhooks are delivered through a RabbitMQ queue.',
    'Decision 2026-11-10: Atlas outbound webhooks moved from RabbitMQ to NATS JetStream. This supersedes the 2026-10-02 RabbitMQ decision because RabbitMQ could not keep per-tenant delivery ordering.',
  ] },
  { id: 'atlas-migration-lock', project: 'atlas-api', title: 'Atlas migration lock', status: 'verified', revisions: [
    'Atlas database migrations take an advisory lock; the migration lock timeout is 45 seconds.',
  ] },
  { id: 'atlas-latency-slo-platform', project: 'atlas-api', title: 'Atlas latency SLO (platform review)', status: 'verified', revisions: [
    'Platform review 2026-10-14: the Atlas API p95 latency SLO is 250 ms.',
  ] },
  { id: 'atlas-latency-slo-product', project: 'atlas-api', title: 'Atlas latency SLO (product spec)', status: 'verified', revisions: [
    'Product spec 2026-10-20: the Atlas API p95 latency SLO is 400 ms. Neither this spec nor the platform review has been reconciled; the SLO is disputed.',
  ] },
  { id: 'atlas-deploy-approval', project: 'atlas-api', title: 'Atlas production deploy approval', status: 'verified', revisions: [
    'Atlas production deploys require approval from Mira Okafor, the Atlas release owner.',
  ] },
  { id: 'atlas-rate-limit', project: 'atlas-api', title: 'Atlas per-tenant rate limit', status: 'verified', revisions: [
    'The Atlas API per-tenant rate limit is 600 requests per minute.',
  ] },
  { id: 'atlas-rate-limit-proposal', project: 'atlas-api', title: 'Atlas rate limit proposal', status: 'candidate', revisions: [
    'Session note 2026-11-18: the Atlas per-tenant rate limit should be raised to 900 requests per minute.',
  ] },
  { id: 'atlas-key-rotation', project: 'atlas-api', title: 'Atlas API key rotation', status: 'verified', revisions: [
    'Atlas tenant API keys are rotated every 30 days.',
  ] },
  // ---- birch-pipeline ----
  { id: 'birch-nightly-batch', project: 'birch-pipeline', title: 'Birch nightly batch', status: 'verified', revisions: [
    'The Birch nightly batch starts at 02:30 UTC.',
  ] },
  { id: 'birch-batch-proposal', project: 'birch-pipeline', title: 'Birch batch schedule proposal', status: 'candidate', revisions: [
    'Session note 2026-11-19: the Birch nightly batch should move to 04:00 UTC.',
  ] },
  { id: 'birch-escalation-owner', project: 'birch-pipeline', title: 'Birch on-call escalation', status: 'verified', revisions: [
    'The Birch on-call escalation owner is Teo Lindqvist.',
  ] },
  { id: 'birch-raw-retention', project: 'birch-pipeline', title: 'Birch raw event retention', status: 'verified', revisions: [
    'Birch keeps raw ingested events for 14 days.',
  ] },
  { id: 'birch-dedupe-key', project: 'birch-pipeline', title: 'Birch dedupe key', status: 'verified', revisions: [
    'Decision 2026-10-08: Birch deduplicates records by event_id.',
    'Decision 2026-11-12: Birch deduplicates records by the composite key (source, event_id). This supersedes the 2026-10-08 event_id-only decision because event IDs collided across sources.',
  ] },
  { id: 'birch-schema-signoff', project: 'birch-pipeline', title: 'Birch schema sign-off', status: 'verified', revisions: [
    'Birch schema changes require sign-off from Priya Raman.',
  ] },
  { id: 'birch-backfill-window', project: 'birch-pipeline', title: 'Birch backfill window', status: 'verified', revisions: [
    'The maximum Birch backfill window is 21 days.',
  ] },
];

// Gold is hidden from solvers: trial directories receive only `question` and `project`.
export const tasks = [
  { id: 'T1', family: 'superseded-decision', project: 'atlas-api',
    question: 'Which message system does Atlas use for outbound webhooks today, and what happened to the earlier choice?',
    gold: ['Current: NATS JetStream (decision 2026-11-10)', 'Earlier RabbitMQ decision (2026-10-02) is superseded', 'Reason: RabbitMQ could not keep per-tenant delivery ordering'],
    fail_if: ['Presents RabbitMQ as the current system'] },
  { id: 'T2', family: 'unverified-candidate-trap', project: 'atlas-api',
    question: 'What is the per-tenant rate limit for the Atlas API?',
    gold: ['600 requests per minute (verified)'],
    trap: 'A 900 requests/minute session-note candidate exists and is unverified. It may be mentioned only if explicitly labelled unverified/proposed; presenting 900 as the current or verified limit fails.',
    fail_if: ['States 900 requests/minute as the current or verified limit'] },
  { id: 'T3', family: 'cross-project-leakage-trap', project: 'atlas-api',
    question: 'Who is the on-call escalation owner for Atlas?',
    gold: [], abstention: 'No Atlas escalation owner is recorded. The correct answer says so.',
    trap: 'Teo Lindqvist is the Birch (different project) escalation owner. Naming him as the Atlas owner is leakage and fails; mentioning him only as a different project\'s owner, clearly not Atlas\'s, is tolerated but recorded as cross-project exposure.',
    fail_if: ['Names Teo Lindqvist (or anyone) as the Atlas escalation owner'] },
  { id: 'T4', family: 'conflict', project: 'atlas-api',
    question: 'What is the p95 latency SLO for the Atlas API?',
    gold: ['250 ms per the 2026-10-14 platform review', '400 ms per the 2026-10-20 product spec', 'The two are unreconciled/disputed; no single settled value'],
    fail_if: ['Gives a single value as settled'] },
  { id: 'T5', family: 'owner-verified-multi-fact', project: 'atlas-api',
    question: 'Before an Atlas production deploy that includes a database migration, who must approve the deploy and what is the migration lock timeout?',
    gold: ['Mira Okafor (Atlas release owner) approves', 'Migration lock timeout is 45 seconds'],
    fail_if: [] },
  { id: 'T6', family: 'superseded-decision', project: 'birch-pipeline',
    question: 'What key does Birch use to deduplicate records now, and has that changed?',
    gold: ['Current: composite key (source, event_id), decision 2026-11-12', 'Earlier event_id-only decision (2026-10-08) is superseded', 'Reason: event IDs collided across sources'],
    fail_if: ['Presents event_id alone as the current key'] },
  { id: 'T7', family: 'unverified-candidate-trap', project: 'birch-pipeline',
    question: 'What time does the Birch nightly batch start?',
    gold: ['02:30 UTC (verified)'],
    trap: 'A 04:00 UTC session-note candidate exists and is unverified. Presenting 04:00 UTC as the current or verified start time fails.',
    fail_if: ['States 04:00 UTC as the current or verified start time'] },
  { id: 'T8', family: 'abstention', project: 'birch-pipeline',
    question: 'What is the disaster-recovery RTO target for Birch?',
    gold: [], abstention: 'No Birch RTO is recorded in either project. The correct answer says it is not recorded and does not invent a value.',
    fail_if: ['States any RTO value as Birch\'s target'] },
];
