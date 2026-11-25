"""Wire contract of the kb HTTP gateway and the C1 collector handoff (no network)."""

from __future__ import annotations

import json
from collections.abc import Callable, Sequence

import httpx
import pytest

from kb_memory.gateway import (
    ALLOWED_OPERATIONS,
    CollectorCaptureGateway,
    HttpMemoryGateway,
    canonical_origin,
)
from kb_memory.models import (
    CandidateDraft,
    ErrorCode,
    Event,
    EvidenceRef,
    MemoryApiError,
    Scope,
)

from .conftest import BEARER

Handler = Callable[[httpx.Request], httpx.Response]

REF = {
    "artifact_id": "artifact-1",
    "digest": "d" * 64,
    "locator": "notes.md#bytes=0-10",
    "source_revision": "source-v1",
    "origin": {"client_id": "collector", "trust": "submitted"},
    "applicability": {},
}


def chunk(status: str = "verified", **extra: object) -> dict[str, object]:
    return {
        "record_id": "claim-1",
        "revision": "rev-1",
        "text": "Synthetic fact.",
        "claim_status": status,
        "origin_trust": "submitted",
        "verification_outcome": "supported",
        "evidence": [REF],
        "warnings": [],
        **extra,
    }


def envelope(*chunks: dict[str, object]) -> dict[str, object]:
    return {
        "schema_version": "3",
        "chunks": list(chunks),
        "truncated": False,
        "visited": len(chunks),
        "no_results": not chunks,
    }


def gateway(handler: Handler, seen: list[httpx.Request] | None = None) -> HttpMemoryGateway:
    def record(request: httpx.Request) -> httpx.Response:
        if seen is not None:
            seen.append(request)
        return handler(request)

    client = httpx.Client(transport=httpx.MockTransport(record), follow_redirects=False)
    return HttpMemoryGateway("https://memory.example.com", BEARER, client)


def problem(status: int, code: str) -> Handler:
    return lambda _: httpx.Response(
        status, json={"code": code, "message": "remote text never relayed"}
    )


SCOPE = Scope("fixture-project", None, "session-1")


def test_orient_sends_schema_v3_request_and_returns_domain_chunks() -> None:
    seen: list[httpx.Request] = []
    body = json.dumps(envelope(chunk())).encode()
    gw = gateway(lambda _: httpx.Response(200, content=body), seen)

    result = gw.orient(SCOPE, "retries", 4096)

    request = seen[0]
    assert (request.method, request.url.path) == ("POST", "/v1/retrieval:orient")
    assert request.headers["Authorization"] == f"Bearer {BEARER}"
    assert json.loads(request.content) == {
        "scope": {"project_id": "fixture-project", "session_id": "session-1"},
        "budget_bytes": 4096,
        "mode": "orientation",
        "query": "retries",
    }
    assert result.evidence_revision_ids == ("rev-1",)
    assert result.source_revisions == ("source-v1",)
    assert result.bytes == len(body)


@pytest.mark.parametrize(
    ("handler", "expected"),
    [
        (problem(401, "unauthorized"), ErrorCode.UNAUTHORIZED),
        (problem(403, "forbidden"), ErrorCode.FORBIDDEN),
        (problem(409, "conflict"), ErrorCode.CONFLICT),
        (problem(409, "stale"), ErrorCode.STALE),
        (problem(410, "deleted"), ErrorCode.DELETED),
        (problem(413, "budget"), ErrorCode.BUDGET),
        (problem(400, "invalid_input"), ErrorCode.INVALID_INPUT),
        (problem(503, "incomplete"), ErrorCode.INCOMPLETE),
        (problem(500, "unavailable"), ErrorCode.UNAVAILABLE),
        (
            lambda _: httpx.Response(302, headers={"Location": "https://elsewhere.example"}),
            ErrorCode.UNAVAILABLE,
        ),
        (lambda _: httpx.Response(200, content=b"x" * 5000), ErrorCode.BUDGET),
        (lambda _: httpx.Response(200, json={"schema_version": "2"}), ErrorCode.UNAVAILABLE),
        (lambda _: httpx.Response(200, json=envelope(chunk("candidate"))), ErrorCode.UNAVAILABLE),
        (
            lambda _: httpx.Response(
                200,
                json=envelope(chunk(passage={"start_byte": 0, "end_byte": 1, "source_bytes": 1})),
            ),
            ErrorCode.UNAVAILABLE,
        ),
    ],
)
def test_orient_fails_closed(handler: Handler, expected: ErrorCode) -> None:
    gw = gateway(handler)

    with pytest.raises(MemoryApiError) as err:
        gw.orient(SCOPE, "q", 4096)

    assert err.value.code == expected
    assert "remote text" not in str(err.value)


def test_transport_error_is_unavailable() -> None:
    def boom(_: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("synthetic")

    with pytest.raises(MemoryApiError) as err:
        gateway(boom).search(SCOPE, "q", 16384)

    assert err.value.code == ErrorCode.UNAVAILABLE


@pytest.mark.parametrize(("op", "budget"), [("orient", 4097), ("search", 16385), ("orient", 0)])
def test_budget_above_contract_is_rejected_before_network(op: str, budget: int) -> None:
    seen: list[httpx.Request] = []
    gw = gateway(lambda _: httpx.Response(200, json=envelope()), seen)

    with pytest.raises(MemoryApiError):
        getattr(gw, op)(SCOPE, "q", budget)

    assert seen == []


def passage_chunk(**overrides: object) -> dict[str, object]:
    base = chunk(
        "candidate",
        record_id="artifact-1",
        passage={"start_byte": 0, "end_byte": 10, "source_bytes": 10},
        warnings=["source_evidence_is_not_a_verified_claim"],
    )
    base.pop("verification_outcome")
    return {**base, **overrides}


def test_fetch_returns_source_passage_as_unverified_evidence() -> None:
    seen: list[httpx.Request] = []
    gw = gateway(lambda _: httpx.Response(200, json=envelope(passage_chunk())), seen)

    result = gw.fetch("artifact-1", "notes.md#bytes=0-10", 16384)

    assert seen[0].url.path == "/v1/evidence/artifact-1"
    assert dict(seen[0].url.params) == {
        "locator": "notes.md#bytes=0-10",
        "budget_bytes": "16384",
        "mode": "publication",
    }
    assert result.chunks[0].claim_status == "candidate"


@pytest.mark.parametrize(
    "body",
    [
        envelope(passage_chunk(record_id="other")),
        envelope(passage_chunk(warnings=[])),
        envelope(chunk()),
        envelope(),
    ],
)
def test_fetch_rejects_mismatched_evidence(body: dict[str, object]) -> None:
    with pytest.raises(MemoryApiError):
        gateway(lambda _: httpx.Response(200, json=body)).fetch(
            "artifact-1", "notes.md#bytes=0-10", 16384
        )


def candidate_response(status: str) -> Handler:
    claim = {
        "claim_id": "hermes-1",
        "revision": "rev-c",
        "text": "t",
        "kind": "finding",
        "scope": {"project_id": "p", "task_id": "t"},
        "evidence": [],
        "depends_on": [],
        "applicability": {},
        "claim_status": status,
        "origin_trust": "submitted",
    }
    return lambda _: httpx.Response(201, json={"schema_version": "3", "claim": claim})


DRAFT = CandidateDraft(
    claim_id="hermes-1",
    text="Synthetic finding.",
    kind="finding",
    scope=Scope("p", "hermes-session-1", "session-1"),
    evidence=(EvidenceRef("artifact-1", "d" * 64, "notes.md", "source-v1"),),
    depends_on=("rev-1",),
)


def test_candidate_write_never_carries_status_or_origin() -> None:
    seen: list[httpx.Request] = []

    receipt = gateway(candidate_response("candidate"), seen).submit_candidate(DRAFT)

    body = json.loads(seen[0].content)
    assert seen[0].url.path == "/v1/candidates"
    assert "claim_status" not in body
    assert "origin" not in body["evidence"][0]
    assert body["depends_on"] == ["rev-1"]
    assert receipt.claim_status == "candidate"


def test_candidate_reported_verified_is_rejected() -> None:
    with pytest.raises(MemoryApiError):
        gateway(candidate_response("verified")).submit_candidate(DRAFT)


def test_gateway_only_reaches_retrieval_and_candidate_paths() -> None:
    seen: list[httpx.Request] = []

    def any_ok(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/v1/candidates":
            return candidate_response("candidate")(request)
        if request.url.path.startswith("/v1/evidence/"):
            return httpx.Response(200, json=envelope(passage_chunk()))
        return httpx.Response(200, json=envelope(chunk()))

    gw = gateway(any_ok, seen)
    gw.orient(SCOPE, "q", 4096)
    gw.search(SCOPE, "q", 16384)
    gw.fetch("artifact-1", "notes.md#bytes=0-10", 16384)

    gw.submit_candidate(DRAFT)

    paths = {(r.method, r.url.path) for r in seen}
    assert paths <= {(m, p.replace("{id}", "artifact-1")) for m, p in ALLOWED_OPERATIONS}
    assert not [p for _, p in paths if "publication" in p or "decision" in p or "verif" in p]


@pytest.mark.parametrize(
    ("value", "expected"),
    [
        ("https://Memory.Example.com", "https://memory.example.com"),
        ("http://127.0.0.1:8080", "http://127.0.0.1:8080"),
        ("http://memory.example.com", None),
        ("https://memory.example.com/path", None),
        ("https://user:pw@memory.example.com", None),
        ("https://memory.example.com?x=1", None),
    ],
)
def test_canonical_origin(value: str, expected: str | None) -> None:
    assert canonical_origin(value) == expected


@pytest.mark.parametrize(
    ("endpoint", "bearer"), [("http://memory.example.com", BEARER), ("https://m.example", "short")]
)
def test_invalid_gateway_configuration_is_refused(endpoint: str, bearer: str) -> None:
    with pytest.raises(ValueError, match="invalid configuration"):
        HttpMemoryGateway(endpoint, bearer)


EVENT = Event(
    id="e1",
    idempotency_key="k1",
    scope=Scope("fixture-project", None, "session-1"),
    kind="message",
    observed_at="2026-11-24T00:00:00Z",
    sequence=0,
    source_locator="hermes:session/session-1#event=0",
    source_path="hermes/sessions/session-1",
    content={"role": "user", "text": "hi"},
)


class FakeRunner:
    def __init__(self, outputs: list[bytes]) -> None:
        self.outputs = outputs
        self.calls: list[tuple[list[str], bytes]] = []

    def __call__(self, argv: Sequence[str], stdin: bytes, timeout: float) -> tuple[int, bytes]:
        self.calls.append((list(argv), stdin))
        return 0, self.outputs.pop(0) if self.outputs else b'{"ok":true,"value":{}}'


def test_events_go_to_local_collector_in_c1_shape() -> None:
    runner = FakeRunner([b'{"ok":true,"value":{"id":"e1","state":"queued"}}'])
    collector = CollectorCaptureGateway("kb", "https://memory.example.com", "default", runner)

    receipt = collector.submit([EVENT])

    argv, stdin = runner.calls[0]
    assert argv == [
        "kb",
        "capture",
        "enqueue",
        "--endpoint",
        "https://memory.example.com",
        "--profile",
        "default",
    ]
    assert json.loads(stdin) == {
        "id": "e1",
        "idempotencyKey": "k1",
        "scope": {"projectID": "fixture-project", "sessionID": "session-1"},
        "kind": "message",
        "observedAt": "2026-11-24T00:00:00Z",
        "sequence": 0,
        "sourceLocator": "hermes:session/session-1#event=0",
        "sourcePath": "hermes/sessions/session-1",
        "content": {"role": "user", "text": "hi"},
    }
    assert runner.calls[1][0][2] == "flush"
    assert receipt.states == ("queued",)


@pytest.mark.parametrize("output", [b"", b"not json", b'{"ok":false,"error":{"code":"paused"}}'])
def test_collector_failure_is_reported_not_retried_elsewhere(output: bytes) -> None:
    runner = FakeRunner([output])
    collector = CollectorCaptureGateway("kb", "https://memory.example.com", "default", runner)

    receipt = collector.submit([EVENT])

    assert receipt.states == ("failed",)
    assert len(runner.calls) == 1  # no flush, no direct upload fallback
