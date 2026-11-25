"""Gateways to the two external systems the provider touches.

``HttpMemoryGateway`` speaks the kb schema-v3 HTTP API with an *agent*
credential: bounded retrieval, evidence fetch and candidate writes only. It has
no publication, verification, decision, policy or client-management operation,
so the provider cannot promote anything.

``CollectorCaptureGateway`` hands conversation events to the local C1 collector
(``kb capture enqueue``), which applies the shared filtering, redaction and
spooling and owns the collector credential. The provider never holds collector
credentials and never uploads events itself.
"""

from __future__ import annotations

import json
import re
import subprocess
import uuid
from collections.abc import Callable, Sequence
from typing import Protocol
from urllib.parse import quote, urlsplit

import httpx
from pydantic import ValidationError

from kb_memory.models import (
    FETCH_MAX_BYTES,
    ORIENT_MAX_BYTES,
    SEARCH_MAX_BYTES,
    CandidateDraft,
    CandidateReceipt,
    Chunk,
    ErrorCode,
    Event,
    MemoryApiError,
    MemoryResult,
    Receipt,
    Scope,
    WireCandidateResult,
    WireCaptureReceipt,
    WireProblem,
    WireRetrievalResult,
)

_BEARER = re.compile(r"^[a-f0-9]{64}$")
_PROBLEM_LIMIT = 65536
_CANDIDATE_LIMIT = 65536
_TIMEOUT_SECONDS = 20.0

# The only API paths this gateway may call. Everything else (publication,
# verification, decisions, policies, clients, deletions) is out of reach.
ALLOWED_OPERATIONS = frozenset(
    {
        ("POST", "/v1/retrieval:orient"),
        ("POST", "/v1/retrieval:search"),
        ("GET", "/v1/evidence/{id}"),
        ("POST", "/v1/candidates"),
    }
)


class MemoryGateway(Protocol):
    """Consumer-defined port used by the conversation service."""

    def orient(self, scope: Scope, query: str, budget_bytes: int) -> MemoryResult: ...

    def search(self, scope: Scope, query: str, budget_bytes: int) -> MemoryResult: ...

    def fetch(self, record_id: str, locator: str, budget_bytes: int) -> MemoryResult: ...

    def submit_candidate(self, draft: CandidateDraft) -> CandidateReceipt: ...


class CaptureGateway(Protocol):
    def submit(self, events: list[Event]) -> Receipt: ...


def canonical_origin(value: str) -> str | None:
    """HTTPS origin only; plain HTTP is allowed for numeric loopback testing."""
    try:
        u = urlsplit(value)
    except ValueError:
        return None
    if u.username or u.password or u.query or u.fragment or u.path not in ("", "/"):
        return None
    host = u.hostname or ""
    if u.scheme == "https" and host:
        return f"https://{u.netloc.lower()}"
    if u.scheme == "http" and host in ("127.0.0.1", "::1"):
        return f"http://{u.netloc.lower()}"
    return None


def valid_bearer(token: str) -> bool:
    return bool(_BEARER.fullmatch(token))


def _scope_wire(scope: Scope) -> dict[str, str]:
    wire = {"project_id": scope.project_id}
    if scope.task_id:
        wire["task_id"] = scope.task_id
    if scope.session_id:
        wire["session_id"] = scope.session_id
    return wire


def _status_error(status: int, code: ErrorCode | None) -> ErrorCode:
    if status == 401:
        return ErrorCode.UNAUTHORIZED
    if status == 403:
        return ErrorCode.FORBIDDEN
    if status == 409:
        return ErrorCode.CONFLICT if code == ErrorCode.CONFLICT else ErrorCode.STALE
    if status == 410:
        return ErrorCode.DELETED
    if status == 413 and code == ErrorCode.BUDGET:
        return ErrorCode.BUDGET
    if status in (400, 413):
        return ErrorCode.INVALID_INPUT
    if status == 503 and code == ErrorCode.INCOMPLETE:
        return ErrorCode.INCOMPLETE
    return ErrorCode.UNAVAILABLE


class HttpMemoryGateway:
    """Agent-role kb API client. Never follows redirects; reads bounded bodies."""

    def __init__(self, endpoint: str, bearer: str, client: httpx.Client | None = None) -> None:
        origin = canonical_origin(endpoint)
        if origin is None or not valid_bearer(bearer):
            raise ValueError("invalid configuration")
        self._origin = origin
        self._bearer = bearer
        self._client = client or httpx.Client(timeout=_TIMEOUT_SECONDS, follow_redirects=False)

    def close(self) -> None:
        self._client.close()

    def _request(
        self,
        method: str,
        template: str,
        path: str,
        limit: int,
        body: dict[str, object] | None = None,
        params: dict[str, str] | None = None,
    ) -> tuple[bytes, int]:
        if (method, template) not in ALLOWED_OPERATIONS:
            raise MemoryApiError(ErrorCode.INVALID_INPUT)
        headers = {
            "Authorization": f"Bearer {self._bearer}",
            "X-Request-Id": str(uuid.uuid4()),
        }
        content: bytes | None = None
        if body is not None:
            headers["Content-Type"] = "application/json"
            content = json.dumps(body, separators=(",", ":")).encode()
        try:
            with self._client.stream(
                method,
                self._origin + path,
                headers=headers,
                content=content,
                params=params,
                follow_redirects=False,
            ) as response:
                status = response.status_code
                cap = limit if status in (200, 201) else _PROBLEM_LIMIT
                raw = bytearray()
                for part in response.iter_bytes():
                    raw.extend(part)
                    if len(raw) > cap:
                        raise MemoryApiError(
                            ErrorCode.BUDGET if status in (200, 201) else ErrorCode.UNAVAILABLE
                        )
        except httpx.HTTPError as exc:
            raise MemoryApiError(ErrorCode.UNAVAILABLE) from exc
        if status in (200, 201):
            return bytes(raw), status
        code: ErrorCode | None = None
        try:
            code = WireProblem.model_validate_json(bytes(raw)).code
        except ValidationError:
            code = None  # never forward remote text
        raise MemoryApiError(_status_error(status, code))

    @staticmethod
    def _envelope(raw: bytes) -> WireRetrievalResult:
        try:
            return WireRetrievalResult.model_validate_json(raw)
        except ValidationError as exc:
            raise MemoryApiError(ErrorCode.UNAVAILABLE) from exc

    def _retrieve(
        self, template: str, scope: Scope, query: str, budget: int, cap: int, mode: str
    ) -> MemoryResult:
        if not 1 <= budget <= cap:
            raise MemoryApiError(ErrorCode.INVALID_INPUT)
        body: dict[str, object] = {
            "scope": _scope_wire(scope),
            "budget_bytes": budget,
            "mode": mode,
        }
        if query:
            body["query"] = query
        raw, _ = self._request("POST", template, template, budget, body=body)
        envelope = self._envelope(raw)
        chunks: list[Chunk] = []
        for wire in envelope.chunks:
            # Published (non-working) retrieval never legitimately returns task
            # candidates or source passages; fail closed instead of labeling them.
            if wire.passage is not None or wire.claim_status not in ("verified", "disputed"):
                raise MemoryApiError(ErrorCode.UNAVAILABLE)
            chunks.append(wire.to_domain())
        return MemoryResult(
            chunks=tuple(chunks),
            bytes=len(raw),
            truncated=envelope.truncated,
            no_results=envelope.no_results,
        )

    def orient(self, scope: Scope, query: str, budget_bytes: int) -> MemoryResult:
        return self._retrieve(
            "/v1/retrieval:orient", scope, query, budget_bytes, ORIENT_MAX_BYTES, "orientation"
        )

    def search(self, scope: Scope, query: str, budget_bytes: int) -> MemoryResult:
        return self._retrieve(
            "/v1/retrieval:search", scope, query, budget_bytes, SEARCH_MAX_BYTES, "evidence"
        )

    def fetch(self, record_id: str, locator: str, budget_bytes: int) -> MemoryResult:
        if not 1 <= budget_bytes <= FETCH_MAX_BYTES or not record_id or not locator:
            raise MemoryApiError(ErrorCode.INVALID_INPUT)
        path = "/v1/evidence/" + quote(record_id, safe="")
        params = {"locator": locator, "budget_bytes": str(budget_bytes), "mode": "publication"}
        raw, _ = self._request("GET", "/v1/evidence/{id}", path, budget_bytes, params=params)
        envelope = self._envelope(raw)
        if len(envelope.chunks) != 1:
            raise MemoryApiError(ErrorCode.UNAVAILABLE)
        wire = envelope.chunks[0]
        # A fetched passage is source evidence, never a verified claim.
        if (
            wire.record_id != record_id
            or wire.passage is None
            or wire.claim_status != "candidate"
            or "source_evidence_is_not_a_verified_claim" not in wire.warnings
        ):
            raise MemoryApiError(ErrorCode.UNAVAILABLE)
        return MemoryResult(
            chunks=(wire.to_domain(),), bytes=len(raw), truncated=envelope.truncated
        )

    def submit_candidate(self, draft: CandidateDraft) -> CandidateReceipt:
        body: dict[str, object] = {
            "claim_id": draft.claim_id,
            "text": draft.text,
            "kind": draft.kind,
            "scope": _scope_wire(draft.scope),
            "evidence": [
                {
                    "artifact_id": e.artifact_id,
                    "digest": e.digest,
                    "locator": e.locator,
                    "source_revision": e.source_revision,
                    "applicability": {},
                }
                for e in draft.evidence
            ],
            "depends_on": list(draft.depends_on),
            "applicability": {},
        }
        raw, _ = self._request(
            "POST", "/v1/candidates", "/v1/candidates", _CANDIDATE_LIMIT, body=body
        )
        try:
            claim = WireCandidateResult.model_validate_json(raw).claim
        except ValidationError as exc:
            raise MemoryApiError(ErrorCode.UNAVAILABLE) from exc
        # A write is never verified by being written.
        if claim.claim_status != "candidate" or claim.claim_id != draft.claim_id:
            raise MemoryApiError(ErrorCode.UNAVAILABLE)
        return CandidateReceipt(
            claim_id=claim.claim_id,
            revision=claim.revision,
            claim_status=claim.claim_status,
            origin_trust=claim.origin_trust,
        )


Runner = Callable[[Sequence[str], bytes, float], tuple[int, bytes]]


def _run(argv: Sequence[str], stdin: bytes, timeout: float) -> tuple[int, bytes]:
    done = subprocess.run(  # noqa: S603 - fixed argv, no shell, configured kb binary
        list(argv), input=stdin, capture_output=True, timeout=timeout, check=False
    )
    return done.returncode, done.stdout


class CollectorCaptureGateway:
    """Hands events to the local kb C1 collector, which filters, spools and uploads."""

    def __init__(self, kb_bin: str, endpoint: str, profile: str, runner: Runner = _run) -> None:
        origin = canonical_origin(endpoint)
        if origin is None or not kb_bin or not profile:
            raise ValueError("invalid configuration")
        self._base = [kb_bin, "capture"]
        self._endpoint = origin
        self._profile = profile
        self._runner = runner

    def _invoke(self, action: str, stdin: bytes, timeout: float) -> WireCaptureReceipt | None:
        argv = [*self._base, action, "--endpoint", self._endpoint, "--profile", self._profile]
        try:
            _, out = self._runner(argv, stdin, timeout)
            return WireCaptureReceipt.model_validate_json(out.strip().splitlines()[-1])
        except (OSError, subprocess.SubprocessError, ValidationError, IndexError):
            return None

    def submit(self, events: list[Event]) -> Receipt:
        ids: list[str] = []
        states: list[str] = []
        for event in events:
            payload = {
                "id": event.id,
                "idempotencyKey": event.idempotency_key,
                "scope": {
                    k: v
                    for k, v in {
                        "projectID": event.scope.project_id,
                        "taskID": event.scope.task_id,
                        "sessionID": event.scope.session_id,
                    }.items()
                    if v
                },
                "kind": event.kind,
                "observedAt": event.observed_at,
                "sequence": event.sequence,
                "sourceLocator": event.source_locator,
                "sourcePath": event.source_path,
                "content": event.content,
            }
            result = self._invoke("enqueue", json.dumps(payload).encode(), 10.0)
            ids.append(event.id)
            if result is None or not result.ok or result.value is None:
                states.append("failed")
            else:
                states.append(result.value.get("state", "failed"))
        if any(s == "queued" for s in states):
            self._invoke("flush", b"", 60.0)
        return Receipt(ids=tuple(ids), states=tuple(states))
