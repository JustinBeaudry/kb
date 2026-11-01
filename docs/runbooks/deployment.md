# Private memory deployment

This runbook covers the three-service O2 slice: API, worker and private Neo4j Community with its supervisor. The owner selected personal Applied Labs, a new `kb-memory` project and `us-east4-eqdc4a`. Show the completed configuration, fixed source identity and locally qualified image identities before the first deployment; record provider-built image identities after deployment when they exist. Existing projects are outside this scope. Model routes and historical capture stay disabled.

## Release and local qualification

`deploy/versions.json` pins tools and base images; `deploy/resource-manifest.json` records sizing, reviewed staged settings, cost assumptions and remaining provider checks. Before first deploy, record the reviewed source commit/base and fixed upload tree SHA256 plus `local_qualified_image_digests`; a base commit alone does not describe uncommitted changes. `provider_deployed_image_digests` remain null until the provider actually builds/deploys that source, then record those digests and exact deployment IDs separately. A CLI source upload does not make its later provider-built image digest available before deployment.

The new project uses direct staged Railway service settings from the manifest, not Config as Code. Current [provider documentation](https://docs.railway.com/config-as-code) says new services cannot opt into Config as Code; `deploy/railway/{api,worker,graph}.toml` are retained only as legacy references. For legacy consumers, [configuration in code takes priority over dashboard settings](https://docs.railway.com/config-as-code/reference), so dashboard edits cannot override an active normal graph file during one-shot initialization. Verify no code-config path is active on this new project. No speculative IaC migration is required by this runbook.

Stage build/start/health/restart settings through `update_service` with `staged=true`. For a service that exists only in the staged patch, instance-dependent connector settings can return `Service Instance not found`. Use the verified `environmentStageChanges(environmentId, input, merge:true)` API for `services.<id>.deploy.multiRegionConfig`, `deploy.limitOverride.containers.cpu` and `memoryBytes`, `deploy.sleepApplication`, and `networking.privateNetworkEndpoint`. The [pinned CLI schema](https://github.com/railwayapp/cli/blob/v5.71.0/src/controllers/config/environment.rs) defines these paths. Preserve the existing patch with `merge:true` and read back every desired value; do not apply partial configuration just to create an instance. Use repository-root source context and the manifest's explicit `dockerfilePath`; set the corresponding nonsecret `RAILWAY_DOCKERFILE_PATH` variable as the documented custom-path mechanism. [Automatic detection expects a root file named Dockerfile](https://docs.railway.com/builds/dockerfiles); these custom paths must not silently fall back to another builder. Verify actual build logs identify the selected Dockerfile. Bucket/domain/volume/key custody remain separately staged resources. API tool builder enums must not be guessed from legacy TOML syntax.

Run the full gate before deployment:

```sh
bun run lint && bun run test
(cd server && go generate ./... && go vet ./... && go tool golangci-lint run ./...)
(cd server && go test -race -timeout 30m -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...)
scripts/check-memory-coverage.sh server/coverage.out
```

CI additionally regenerates the TypeScript API, rejects generated diffs, requires executed restore acceptance and builds `linux/amd64` images. No skipped/absent restore test counts as acceptance. Whole-module coverage deduplicates coverpkg blocks and excludes only files carrying the generated-code declaration.

Start the synthetic fixture in a new owner-only empty directory:

```sh
fixture=$(mktemp -d /tmp/kb-memory.XXXXXXXX)
deploy/compose-smoke.sh up "$fixture"
```

The script builds the compiled binaries, generates fresh disposable credentials, initializes isolated object buckets and uses protected host administration for setup, journal initialization and policy creation. Private `runtime.json` identifies the API, protected scoped credential and exact compose project. It reports build and startup seconds separately; the acceptance suite records authenticated result/restore timing. A green liveness response alone is not first-result acceptance. The script separately waits for the object gateway and private supervisor status (`Status.HostReady`, zero active/unknown operations) before every host mutation. It retries only the exact known pre-admission maintenance refusal; unknown results, delivery errors, mixed output and timeouts stop immediately while preserving the append-only credential sink. Policy input is reopened unchanged on any permitted retry. Private status/operation diagnostics stay in mode0600 fixture files. Local API/object ports bind loopback only. The fixture shares the graph network namespace so the deliberately plaintext fixture S3 endpoint is loopback; production uses separate services and provider HTTPS.

```sh
deploy/compose-smoke.sh down "$fixture"
```

Cleanup targets only that generated compose project and its volumes. The private directory retains credentials until the operator securely removes it. Failed startup leaves its state available for diagnosis and the same exact cleanup command.

## Runtime users and volume initialization

All final images specify UID/GID `7474:7474`. Graph contains `kb-supervisor`, `kb-admin`, Neo4j and pinned local dump/load tools. Supervisor is PID1 and owns the Neo4j child, shutdown and restart. HTTP/browser connectors are disabled; only Bolt 7687 and authenticated control 8091 are private. Do not create a graph public domain or TCP proxy.

The generated Neo4j configuration explicitly selects `db.query.default_language=CYPHER_25`, matching the pinned distribution and qualified queries. This setting applies when a database is created; changing it does not migrate an existing Cypher 5 database or alter the language preserved in a backup. Initial deployment requires a fresh volume. Treat older, unqualified database volumes or snapshots as requiring separate migration and acceptance rather than assuming this configuration change upgrades them.

A Docker named volume copies the image's owner-only `/data/kb` on first use. Railway's [volume permissions and availability documentation](https://docs.railway.com/volumes#permissions) states that volumes mount as root at container start, supports `RAILWAY_RUN_UID=0`, and excludes volumes from pre-deploy commands. Its [Docker start-command documentation](https://docs.railway.com/deployments/start-command) explicitly states that a configured start command replaces the image ENTRYPOINT in exec form. These establish the provider mechanisms; the selected service's actual mount, command override and ownership still require live qualification.

Use this one-time initialization sequence after showing the completed deployment configuration:

1. On the new graph service and its empty `/data` volume, stage the manifest's `graph_initialization` settings directly and temporarily set `RAILWAY_RUN_UID=0`: fixed start command `/usr/local/bin/kb-graph-volume-init`, restart policy `NEVER`, and no HTTP healthcheck. Clear the healthcheck with explicit null through the staged API and inspect staged and resolved values; an empty string is not an established clear operation. No normal graph TOML/config-file path may override these settings. Invoke the fixed initializer directly through the start-command override; do not pass it through the ordinary UID7474 runtime wrapper. Do not use pre-deploy for mounted-volume initialization.
2. Require exit0 and the safe `volume_initialized` marker. The initializer accepts no arguments, never traverses existing entries, refuses a foreign existing owner and exits after setting ownership. A refusal requires inspection, not recursive chown or deleting existing data.
3. Stage the manifest's `graph_normal` settings, remove `RAILWAY_RUN_UID` and clear the temporary start-command override. Use explicit null for the start-command override and verify it is absent in staged and resolved configuration, selecting the image ENTRYPOINT. Restore the reviewed normal graph restart policy and `/livez` deployment check, then deploy the same fixed source/Dockerfile/base pins and volume; record the provider image digest produced by each deployment. The normal image ENTRYPOINT runs the protected materializer and supervisor as UID7474.
4. Verify actual effective UID7474, owner-only writable `/data/kb`, persisted state, private Bolt/control reachability and no public graph ingress. Record the exact initialization and normal deployment IDs separately. Do not label this sequence qualified until those observations exist.

The initializer never becomes the long-running service. A build-time `chown` does not initialize a mounted Railway volume, and the normal deployment must not retain the root override.

Initial graph capacity is 32GiB for an empty/synthetic installation, with an 8GiB headroom target and an alert before 75% utilization. Dump/decrypt/restore must fail closed when measured free space cannot cover required staging; the configured archive bound is not a capacity guarantee. Graph resource caps are 2vCPU/4GiB; API/worker each cap at 0.5vCPU/512MiB. Bucket usage and backup upload traffic are measured, not assumed prepaid capacity. Cost examples in the manifest are separate sustained-use scenarios, not a bill forecast.

The graph volume is private encrypted scratch plus persistent maintenance/process state, not a shared mount. API/worker have no graph mount. No fourth scheduler or Railway cron is attached to the continuous graph service. Volume snapshots are supplemental and do not replace the authenticated 30-day encrypted backup.

## Configuration and ingress

API: `KB_LISTEN=[::]:8080`, `KB_INGRESS_TRANSPORT=railway-edge`, `PORT=8080`. Worker: `KB_WORKER_HEALTH_LISTEN=[::]:8081`, `PORT=8081`, and explicit unique logical `KB_WORKER_ID=kb-memory-worker-1`; no public ingress. Additional worker replicas need distinct logical IDs, while process admission identity remains fresh on every startup. Graph: `KB_SUPERVISOR_ROOT=/data/kb`, `KB_SUPERVISOR_CONTROL_LISTEN=[::]:8091`, `PORT=8091`; one volume `/data`, `KB_BACKUP_MAX_BYTES=4294967296` for the initial bounded archive. Set private graph URI and exact maintenance endpoint for each service. No arbitrary forwarded header enables deployment mode or grants authority. Default `direct` transport continues requiring TLS for nonloopback listeners. The explicit edge mode trusts Railway to terminate HTTPS and isolate origin traffic; measure that actual route before acceptance.

Set graph/database, evidence S3 and independent journal configuration through protected runtime configuration. Configure selected provider `KB_S3_ADDRESSING_STYLE` and `KB_JOURNAL_ADDRESSING_STYLE` explicitly (`virtual` for current Railway buckets; `path` only for qualified endpoints). `KB_BACKUP_ADDRESSING_STYLE` follows the independently configured backup provider. Never use SDK credential discovery, presigned authorization bypass, source text in environment variables or credentials in build arguments. See [credentials](credentials.md).

## First protected host bootstrap

Run as UID7474 inside the graph host. Actual Railway SSH exec starts as root even when PID1 runs as UID7474. Verify PID1 and directory ownership separately, then prefix host commands with `/usr/bin/setpriv --reuid=7474 --regid=7474 --clear-groups` and retain an effective-UID check before opening credential descriptors. This drops the exec process to the runtime owner without changing the normal service configuration. Run each host command through `/usr/local/bin/kb-runtime-entrypoint` when secrets are supplied as `*_CONTENTS`, because a later host-exec process does not inherit PID1's exported file paths. The local Unix socket admits fixed allowlisted `kb-admin` commands under serial supervisor recovery permits; service-role HTTP credentials never authorize owner commands.

1. Start graph closed. Supervisor starts Neo4j and explicitly initializes schema; public `/livez` is healthy and `/readyz` remains 503. API and worker do not independently migrate or initialize a journal.
2. Prepare an owner-only destination or private pipe on the graph host. Open descriptor 3 to that destination, then run `kb-admin setup --owner-id PUBLIC_ID --owner-public-key PUBLIC_KEY --project PROJECT --credential-fd 3`. The arguments contain only public identifiers. Scoped bearer bytes go only to the protected descriptor. Transfer/import the file over a protected channel and remove the host copy.
3. Run `kb-admin journal-init`, then `kb-admin journal-check`. Initialization is only valid for a proven fresh installation. A missing historical journal requires recovery, never reinitialization.
4. Inspect `kb-admin policy` with strict stdin JSON; create the initial policy through `policy-update` with the actual empty `ExpectedRevision`, a new valid revision and model routes omitted. Future updates use the inspected current revision. Policy durations are integer nanoseconds.
5. Supervisor completes live journal recovery and dependency checks before resuming the exact fence. Require authenticated status, synthetic capture, host publication and cited retrieval. Keep historical import off.

On existing or restored installs skip setup/init and follow private status plus [recovery](recovery.md). Unknown process/effect state keeps maintenance closed. Do not clear permits by timeouts or manual state-file edits.

## Health, backup and rollback

Railway initial deployment health uses `/livez` so a fresh host can be bootstrapped while unready. Runtime admission and `/readyz` remain authoritative; [Railway healthchecks](https://docs.railway.com/deployments/healthchecks) do not enforce ongoing readiness. Monitor authenticated private status, journal 9000/10000 capacity and backup age. See [observability](observability.md).

Supervisor schedules at a 20-hour interval from the successful snapshot boundary, leaving completion margin for the 24-hour RPO target. It persists catch-up/retry state on the graph volume. Retain authenticated completed archives for 30 days. RPO ≤24h, RTO ≤4h and maintenance duration require measured drills; outages may violate them. Follow [recovery](recovery.md) for explicit backup and fresh-target restore. Never restore the independent journal from an archive. All restored pre-restore scoped clients must be revoked before admission and host reenrollment.

Rollback replaces API/worker/graph images with a schema-compatible reviewed release while maintenance remains fenced. Do not roll graph/object state backward as an application rollback. Retain prior data and backup generations; restore only to fresh targets, authenticate current independent journal and replay deletions before opening. A failed archive cannot replace the last completed backup. Unknown effects or failed recovery remain closed with liveness available for repair.


## Test-only held-I/O proxy

The local compose fixture includes a disposable proxy; the three production images, Railway TOMLs and service topology never include it. Fixture evidence and independent journal HTTP endpoints are loopback8334, forwarding to the local object server8333. Backups remain direct8333. Graph publishes proxy control8335 to a Docker-assigned host loopback port, discovered as private `runtime.json.proxy_url`; no data or control route is public. Do not configure production storage through this fixture.

`POST /arm` accepts bounded strict JSON: `id`, `method`, `mode` (`hold` or `drop_ack`) plus at least one of `path_prefix`, `path_suffix`, or `header_name` with `header_value`. Selectors are combined with AND; header names are case insensitive and path selectors match the escaped path. One rule matches one request, after its full bounded body arrives and before upstream forwarding. `GET /state` reports only opaque rule ID, mode and safe reached/released/forwarded/completed/count/status/error fields. `POST /release` with the matching `id` releases a held request. Arm/release return200 JSON; active rule replacement or wrong release returns409. Duplicate/unknown fields, trailing JSON and invalid selectors return400. No headers, paths, request/response bodies or secrets are logged or included in status.

Hold is bounded120 seconds. The proxy preserves original signed Host, path, query, headers and body while changing only upstream connection destination. `drop_ack` sends the complete upstream request, consumes the complete bounded response, then closes the client connection without HTTP response. It never retries. Actual client mutation retry policy and unknown-outcome behavior are verified by the parent compiled acceptance, not asserted by this fixture alone. Narrow evidence prefixes to the actual artifact path rather than qualification probes; use `x-amz-meta-kb-deleted: 1` for the worker tombstone and the journal head suffix for host deletion. Require reached, exactly one match, forwarded and a2xx upstream result when claiming the intended effect. The final lost-ack/restart scenario is disposable and must retain an unknown permit rather than reopen maintenance.

CI explicitly runs race tests for `server/internal/acceptance/testdata/proxy` because Go wildcard package discovery excludes testdata. Those real HTTP boundary tests are separate from the final compiled service/backup/drain/restore acceptance.
