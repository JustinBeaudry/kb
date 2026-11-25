"""Conversation memory service: the provider's business rules.

- Retrieval is bounded and cited; claim status labels are preserved verbatim.
- Authorization failures are terminal: never retried with a broader scope.
- An answer that repeats recalled memory is not independent evidence.
- Hermes built-in notes are private working state and never reach the graph.
- Exactly one elected capture owner hands a session's turns to the C1 collector.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import secrets
import threading
import unicodedata
from collections.abc import Callable
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

from kb_memory.gateway import CaptureGateway, MemoryGateway
from kb_memory.models import (
    ORIENT_MAX_BYTES,
    CandidateDraft,
    Chunk,
    ErrorCode,
    Event,
    EvidenceRef,
    MemoryApiError,
    MemoryResult,
    Receipt,
    Scope,
)

_SAFE_ID = re.compile(r"^[A-Za-z0-9._-]{1,128}$")
_ECHO_MIN_CHARS = 24
_ACTIONS = {
    ErrorCode.FORBIDDEN: "This client is not authorized for that scope; do not retry elsewhere.",
    ErrorCode.UNAUTHORIZED: "The kb credential was rejected; ask the owner to re-enroll.",
    ErrorCode.BUDGET: "Request a smaller result or narrow the query.",
    ErrorCode.UNAVAILABLE: "kb memory is unavailable; continue without it.",
}


def normalize(text: str) -> str:
    folded = unicodedata.normalize("NFKC", text).casefold()
    return " ".join(re.sub(r"[^\w\s]", " ", folded).split())


def safe_id(value: str) -> str:
    """Opaque, path- and policy-safe identifier derived from a Hermes id."""
    if _SAFE_ID.fullmatch(value) and value not in (".", ".."):
        return value
    return "h" + hashlib.sha256(value.encode()).hexdigest()[:32]


def _digest(parts: list[object]) -> str:
    return hashlib.sha256(json.dumps(parts, separators=(",", ":")).encode()).hexdigest()


@dataclass(frozen=True)
class ToolOutcome:
    """Structured tool response; the provider serializes it to JSON for Hermes."""

    ok: bool
    payload: dict[str, object]

    def to_json(self) -> str:
        return json.dumps({"ok": self.ok, **self.payload}, separators=(",", ":"))


class CaptureElection:
    """Per-session single capture owner across processes sharing a Hermes home.

    The first live instance to create the session's marker owns capture; others
    never upload that session. A marker left by a dead process is taken over.
    """

    def __init__(self, directory: Path, token: str | None = None) -> None:
        self._dir = directory
        self._token = token or secrets.token_hex(16)
        self._held: set[str] = set()

    @property
    def token(self) -> str:
        return self._token

    def _path(self, session_id: str) -> Path:
        return self._dir / (hashlib.sha256(session_id.encode()).hexdigest() + ".owner")

    def claim(self, session_id: str) -> bool:
        if session_id in self._held:
            return True
        self._dir.mkdir(mode=0o700, parents=True, exist_ok=True)
        path = self._path(session_id)
        record = json.dumps({"pid": os.getpid(), "token": self._token}).encode()
        for _ in range(2):
            try:
                fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            except FileExistsError:
                if not self._stale(path):
                    return False
                path.unlink(missing_ok=True)
                continue
            with os.fdopen(fd, "wb") as f:
                f.write(record)
            self._held.add(session_id)
            return True
        return False

    def _stale(self, path: Path) -> bool:
        try:
            owner = json.loads(path.read_bytes())
            pid = int(owner["pid"])
        except (OSError, ValueError, KeyError, TypeError):
            return False  # unreadable marker: fail closed, do not double-capture
        if pid == os.getpid():
            return False  # another live instance in this process owns it
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return True
        except PermissionError:
            return False
        return False

    def release(self, session_id: str) -> None:
        if session_id not in self._held:
            return
        self._held.discard(session_id)
        path = self._path(session_id)
        try:
            owner = json.loads(path.read_bytes())
            if owner.get("token") == self._token:
                path.unlink(missing_ok=True)
        except (OSError, ValueError):
            return

    def release_all(self) -> None:
        for session_id in list(self._held):
            self.release(session_id)


class ConversationMemoryService:
    def __init__(
        self,
        gateway: MemoryGateway,
        project_id: str,
        *,
        capture: CaptureGateway | None = None,
        election: CaptureElection | None = None,
        prefetch_budget_bytes: int = ORIENT_MAX_BYTES,
        clock: Callable[[], datetime] = lambda: datetime.now(UTC),
    ) -> None:
        if not project_id or not 1 <= prefetch_budget_bytes <= ORIENT_MAX_BYTES:
            raise ValueError("invalid configuration")
        self._gateway = gateway
        self._project = project_id
        self._capture = capture
        self._election = election
        self._budget = prefetch_budget_bytes
        self._clock = clock
        self._lock = threading.Lock()
        self._recalled: dict[str, dict[str, Chunk]] = {}
        self._prefetched: dict[str, tuple[str, MemoryResult]] = {}
        self._sequence: dict[str, int] = {}
        self._local_notes = 0

    # -- scopes ----------------------------------------------------------------

    def scope(self, session_id: str) -> Scope:
        return Scope(
            project_id=self._project, session_id=safe_id(session_id) if session_id else None
        )

    def task_scope(self, session_id: str) -> Scope:
        sid = safe_id(session_id or "unbound")
        return Scope(project_id=self._project, task_id="hermes-" + sid, session_id=sid)

    # -- retrieval -------------------------------------------------------------

    def _remember(self, session_id: str, result: MemoryResult) -> None:
        with self._lock:
            known = self._recalled.setdefault(session_id, {})
            for chunk in result.chunks:
                known[chunk.revision] = chunk

    def _bounded(
        self, session_id: str, budget: int, call: Callable[[], MemoryResult]
    ) -> MemoryResult:
        """One request; a transport failure retries once with the *same* scope."""
        for attempt in range(2):
            try:
                result = call()
            except MemoryApiError as err:
                if err.code == ErrorCode.UNAVAILABLE and attempt == 0:
                    continue
                return MemoryResult(chunks=(), bytes=0, error=err.code)
            if result.bytes > budget:
                # Over-budget responses are discarded, never partially trusted.
                return MemoryResult(chunks=(), bytes=0, error=ErrorCode.BUDGET)
            self._remember(session_id, result)
            return result
        return MemoryResult(chunks=(), bytes=0, error=ErrorCode.UNAVAILABLE)

    def recall(self, query: str, session_id: str, budget_bytes: int | None = None) -> MemoryResult:
        budget = min(budget_bytes or self._budget, self._budget)
        scope = self.scope(session_id)
        return self._bounded(session_id, budget, lambda: self._gateway.orient(scope, query, budget))

    def search(self, query: str, session_id: str, budget_bytes: int) -> MemoryResult:
        scope = self.scope(session_id)
        return self._bounded(
            session_id, budget_bytes, lambda: self._gateway.search(scope, query, budget_bytes)
        )

    def fetch(
        self, record_id: str, locator: str, session_id: str, budget_bytes: int
    ) -> MemoryResult:
        return self._bounded(
            session_id, budget_bytes, lambda: self._gateway.fetch(record_id, locator, budget_bytes)
        )

    def queue_prefetch(self, query: str, session_id: str) -> None:
        result = self.recall(query, session_id)
        with self._lock:
            self._prefetched[session_id] = (query, result)

    def prefetch(self, query: str, session_id: str) -> str:
        with self._lock:
            cached = self._prefetched.pop(session_id, None)
        # A queued result is reused only for the same prompt; never inject stale recall.
        if cached is not None and cached[0] == query:
            result = cached[1]
        else:
            result = self.recall(query, session_id)
        return self.render(result, self._budget)

    # -- presentation ----------------------------------------------------------

    @staticmethod
    def _line(chunk: Chunk) -> str:
        if chunk.claim_status == "verified":
            label = "verified"
        elif chunk.claim_status == "disputed":
            label = "disputed: uncertain, present alternatives, do not state as fact"
        else:
            label = f"{chunk.claim_status}: unverified"
        cites = ", ".join(
            f"{e.artifact_id}@{e.source_revision} ({e.locator})" for e in chunk.evidence
        )
        warn = f" warnings={','.join(chunk.warnings)}" if chunk.warnings else ""
        return f"- [{label}] {chunk.text} [rev:{chunk.revision}; evidence: {cites or 'none'}]{warn}"

    def render(self, result: MemoryResult, budget: int) -> str:
        """Cited recall text whose UTF-8 size never exceeds ``budget``."""
        if result.error or not result.chunks:
            return ""
        header = (
            "kb graph memory. Cite [rev:...] IDs when you use a line. [verified] lines are "
            "authoritative over your built-in notes; other labels are not established fact."
        )
        lines = [header]
        size = len(header.encode())
        dropped = False
        for chunk in result.chunks:
            line = self._line(chunk)
            if size + 1 + len(line.encode()) + 40 > budget:
                dropped = True
                break
            lines.append(line)
            size += 1 + len(line.encode())
        if len(lines) == 1:
            return ""
        if dropped or result.truncated:
            lines.append("- (more memory exists; use kb_memory_search)")
        text = "\n".join(lines)
        return text if len(text.encode()) <= budget else ""

    @staticmethod
    def describe(result: MemoryResult) -> ToolOutcome:
        if result.error:
            return ToolOutcome(
                ok=False,
                payload={"error": result.error.value, "action": _ACTIONS.get(result.error, "")},
            )
        return ToolOutcome(
            ok=True,
            payload={
                "evidence_revision_ids": list(result.evidence_revision_ids),
                "no_results": result.no_results,
                "truncated": result.truncated,
                "bytes": result.bytes,
                "chunks": [
                    {
                        "record_id": c.record_id,
                        "revision": c.revision,
                        "text": c.text,
                        "claim_status": c.claim_status,
                        "origin_trust": c.origin_trust,
                        "verification_outcome": c.verification_outcome,
                        "warnings": list(c.warnings),
                        "evidence": [
                            {
                                "artifact_id": e.artifact_id,
                                "digest": e.digest,
                                "locator": e.locator,
                                "source_revision": e.source_revision,
                                "origin_trust": e.origin_trust,
                            }
                            for e in c.evidence
                        ],
                    }
                    for c in result.chunks
                ],
            },
        )

    # -- echo detection --------------------------------------------------------

    def _echoes(self, session_id: str, text: str) -> list[Chunk]:
        said = normalize(text)
        if not said:
            return []
        with self._lock:
            known = list(self._recalled.get(session_id, {}).values())
        hits = []
        for chunk in known:
            recalled = normalize(chunk.text)
            if not recalled:
                continue
            if (
                said == recalled
                or (
                    min(len(said), len(recalled)) >= _ECHO_MIN_CHARS
                    and (recalled in said or said in recalled)
                )
                or chunk.revision in text
            ):
                hits.append(chunk)
        return hits

    # -- candidate writes ------------------------------------------------------

    def submit_candidate(
        self,
        session_id: str,
        text: str,
        kind: str,
        evidence: tuple[EvidenceRef, ...],
        depends_on: tuple[str, ...],
    ) -> ToolOutcome:
        echoed = self._echoes(session_id, text)
        if echoed:
            known = {(e.artifact_id, e.source_revision) for c in echoed for e in c.evidence}
            new = [e for e in evidence if (e.artifact_id, e.source_revision) not in known]
            if not new:
                # Repeating recalled memory adds no independent corroboration.
                return ToolOutcome(
                    ok=True,
                    payload={
                        "submitted": False,
                        "reason": "echo_of_recalled_memory",
                        "evidence_revision_ids": [c.revision for c in echoed],
                    },
                )
            depends_on = tuple(dict.fromkeys([*depends_on, *(c.revision for c in echoed)]))
            # Recalled support becomes a dependency; only task-scoped new evidence is sent,
            # since the server rejects evidence whose artifact scope differs from the claim's.
            evidence = tuple(new)
        scope = self.task_scope(session_id)
        draft = CandidateDraft(
            claim_id="hermes-" + _digest(["candidate", scope.task_id, text, kind])[:40],
            text=text,
            kind=kind,
            scope=scope,
            evidence=evidence,
            depends_on=depends_on,
        )
        try:
            receipt = self._gateway.submit_candidate(draft)
        except MemoryApiError as err:
            return ToolOutcome(
                ok=False, payload={"error": err.code.value, "action": _ACTIONS.get(err.code, "")}
            )
        return ToolOutcome(
            ok=True,
            payload={
                "submitted": True,
                "claim_id": receipt.claim_id,
                "revision": receipt.revision,
                "claim_status": receipt.claim_status,
                "origin_trust": receipt.origin_trust,
                "note": "Candidates are unverified until independently verified and published.",
            },
        )

    # -- built-in notes --------------------------------------------------------

    def note_written(self) -> None:
        """Built-in Hermes notes stay local working state: never uploaded or promoted."""
        with self._lock:
            self._local_notes += 1

    # -- capture ---------------------------------------------------------------

    def capture_turn(
        self, session_id: str, user_content: str, assistant_content: str
    ) -> Receipt | None:
        if self._capture is None or self._election is None or not session_id:
            return None
        if not self._election.claim(session_id):
            return None  # another owner captures this session
        sid = safe_id(session_id)
        with self._lock:
            start = self._sequence.get(session_id, 0)
            self._sequence[session_id] = start + 2
        derived = [c.revision for c in self._echoes(session_id, assistant_content)]
        now = self._clock().isoformat().replace("+00:00", "Z")
        scope = Scope(project_id=self._project, session_id=sid)
        events = []
        for offset, (role, text) in enumerate(
            (("user", user_content), ("assistant", assistant_content))
        ):
            seq = start + offset
            content: dict[str, object] = {"harness": "hermes", "role": role, "text": text}
            if role == "assistant":
                # Answers are derived output, never independent observations.
                content["independent_observation"] = False
                content["derived_from_revisions"] = derived
            key = _digest(["hermes-capture-1", sid, self._election.token, seq])
            events.append(
                Event(
                    id=key,
                    idempotency_key=key,
                    scope=scope,
                    kind="message",
                    observed_at=now,
                    sequence=seq,
                    source_locator=f"hermes:session/{sid}#event={seq}",
                    source_path=f"hermes/sessions/{sid}",
                    content=content,
                )
            )
        return self._capture.submit(events)

    def end_session(self, session_id: str) -> None:
        with self._lock:
            self._recalled.pop(session_id, None)
            self._prefetched.pop(session_id, None)
        self.release_capture(session_id)

    def release_capture(self, session_id: str) -> None:
        """Give up capture ownership so another process may capture this session."""
        if self._election is not None:
            self._election.release(session_id)

    def close(self) -> None:
        with self._lock:
            self._recalled.clear()
            self._prefetched.clear()
        if self._election is not None:
            self._election.release_all()
