# Buzz owner decisions and recovery

Buzz carries owner decisions about candidate memory. A decision binds one owner, one candidate revision, one evidence digest (claim state plus the exact generation of every evidence artifact), one requested action and a 24-hour expiry. Approving one revision never approves a later one. The owner's answer is recorded as an `OwnerJudgment`, which is separate from empirical `Verification`. A judgment never marks a claim verified by itself.

## Identity and credentials

KB uses its own Buzz bot identity. Never reuse Software Factory, personal desktop or other project keys.

| Setting | Meaning |
|---|---|
| `KB_BUZZ_BINARY` | Absolute path of the pinned `buzz` CLI |
| `KB_BUZZ_RELAY_URL` | `https://` relay base URL (plain `http://` is accepted only for localhost) |
| `KB_BUZZ_CHANNEL` | Private decision channel UUID; the bot and the owner must both be members |
| `KB_BUZZ_PRIVATE_KEY_FILE` | Owner-only (mode 0600) regular file holding the bot's 64-hex secp256k1 secret |

If none of these are set, decisions remain pending and only the recovery path below can resolve them. The owner's public key is the one enrolled with `kb-admin setup --owner-public-key`; replies from any other key have no effect.

The gateway starts the CLI directly (no shell) with exactly this environment: `PATH=/usr/bin:/bin`, `LANG=C`, `BUZZ_OUTPUT_FORMAT=json`, `BUZZ_RELAY_URL` and `BUZZ_PRIVATE_KEY`. Because an explicit key is always set, the CLI cannot fall back to a desktop-paired identity. `HOME` and every other inherited variable are withheld. The message body goes over stdin (`--content -`). The key never appears in argv. Error text is cut to 256 bytes, and the key and any 64-hex or `nsec` value are redacted from it. Each call has a 30-second timeout and 4 MiB of output.

## Requesting a decision

On the service host, the owner opens a decision with `kb-admin decision-request`, in two steps like the other kb-admin commands:

1. **Preview.** Run `kb-admin decision-request --preview` with `{"CandidateRevision":"…","RequestedAction":"publish"}`. This is read-only. It checks that the revision is the current candidate head and prints confirmation JSON with the current `ExpectedEvidenceDigest`.
2. **Apply.** Review the preview, then run `kb-admin decision-request` with that JSON unchanged. The service checks the digest again. If the revision or any evidence artifact changed in between, the request fails `stale` and nothing is recorded. Otherwise the decision is saved as `pending` and posted to Buzz once.

Repeating the same request returns the same decision. After a decision expires, the same request opens a new 24-hour decision with a new ID. The old one is marked `expired` and is never posted. A resolved decision is returned as-is.

## Delivery and replies

`kb-admin decision-sync` is the one entry point for delivery and polling. KB has no built-in scheduler, and a scheduled job is out of scope for this change. The operator runs it by hand or from a host timer they control (for example a systemd timer or cron entry every few minutes). Running it often is safe: delivery and reply handling are idempotent.


`kb-admin decision-sync` (input `{"Limit":50}`) does two things:

1. **Delivery.** It posts every undelivered pending decision. Each post ends with `kb-op: kb-decision-<id>`. Before posting, and again after any ambiguous failure (network exit 2, timeout, unparsable acknowledgement), the gateway searches the channel for a bot-signed event that carries that operation marker. A lost acknowledgement or a retry therefore never creates a second post. If the outcome cannot be reconciled, the decision stays undelivered and the result is `unknown_outcome`. Run sync again later. Usage and auth exits (1 and 3) are definitive failures.
2. **Polling.** It fetches channel messages from the stored cursor, or from 25 hours back the first time. Before anything reaches the service, the gateway checks each event locally: the NIP-01 event ID, the BIP-340 signature and the channel `h` tag. Relay and CLI claims are never trusted. A reply counts only when all of these hold:
   - It is signed by the enrolled owner key.
   - It is in the decision's own thread (NIP-10 root equal to the post's event ID).
   - It is exactly one line, `approve|reject <decision-id> <candidate-revision> <evidence-digest>`, which the post provides ready to copy.

   Duplicate event IDs return the earlier receipt and have no further effect. A different event on an already-resolved decision is a conflict.

   The poll cursor is a timestamp, and authors choose their own event timestamps. So the cursor never moves past the host clock. Events dated more than 5 minutes ahead of the host clock are dropped, and a stored cursor that is already in the future is treated as absent. If a valid owner reply fails for a transient reason (graph, transaction or evidence-read error), sync counts it as `Deferred`, reports the error and leaves the cursor where it was, so the next sync polls that reply again. Only permanent rejections (wrong signer, thread or binding, stale, expired, already resolved) let the cursor move past a reply.

Posts contain identifiers only. Claim text and evidence content never go to Buzz. Inspect content on the host with `kb-admin decisions`.

Every resolution re-checks the current candidate head, its status and the artifact generations inside the resolving graph transaction. A changed revision or changed evidence marks the decision `stale`. An answer after expiry marks it `expired`. Both return `stale` to the caller.

## Outage behaviour

A Buzz or relay outage never fails a decision request. The decision is saved as `pending` with no thread, while capture, retrieval and other work continue. `decision-sync` reports `unavailable` and leaves everything pending. Once Buzz recovers, run sync again. Expired decisions are not delivered.

## Owner recovery without Buzz

Recovery never gives an owner credential to the agent process.

1. On the service host, run `kb-admin decisions` to list pending decisions.
2. Run `kb-admin decision-challenge` with `{"DecisionID":"…","Action":"approve"}`. It prints a challenge (ID, random nonce, 10-minute expiry and action digest; no bearer) and a `SigningDigest`.
3. On the owner's own device, sign the 32-byte `SigningDigest` with the enrolled key (BIP-340 Schnorr). Never bring the private key to the service host or into an agent session.
4. Run `kb-admin decision-approve` with `{"ChallengeID":"…","DecisionID":"…","Action":"approve","Signature":"<128 hex>"}`.

The service verifies the signature against the enrolled owner key. It then mints a one-use grant bound to exactly that decision, action, revision and evidence digest, and redeems the grant immediately. Only the grant's token digest is stored, and the token is never printed. A signature from an agent's key is refused. A grant cannot be reused, cannot be used for another action or decision, and carries no role, project or capability.

`POST /v1/decisions/{id}:respond` is the API form of the same signed answer. The owner signs `sha256("kb-decision-reply/v1\n" + id + "\n" + candidate_revision + "\n" + evidence_digest + "\n" + action + "\n" + event_id)`, where `event_id` is a fresh nonce. The caller's bearer grants no authority on this route; only the owner signature does.

## Acceptance status

Local tests use a synthetic signed relay fixture with test-only generated keys. No real account, relay or channel was contacted. An actual private Buzz post and reply round trip runs only in O3, against the configured KB identity and channel.
