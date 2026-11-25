"""Synthetic fixtures. Fakes stand in only at the gateway boundary."""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import pytest

from kb_memory.models import (
    CandidateDraft,
    CandidateReceipt,
    Chunk,
    ErrorCode,
    Event,
    EvidenceRef,
    MemoryApiError,
    MemoryResult,
    Receipt,
    Scope,
    Settings,
)
from kb_memory.provider import KbMemoryProvider

BEARER = "a" * 64
PROJECT = "fixture-project"

VERIFIED = Chunk(
    record_id="claim-webhook-retries",
    revision="rev-verified-1",
    text="The synthetic billing service retries webhooks three times.",
    claim_status="verified",
    origin_trust="submitted",
    verification_outcome="supported",
    evidence=(
        EvidenceRef(
            artifact_id="artifact-billing-notes",
            digest="d" * 64,
            locator="billing/notes.md#bytes=0-64",
            source_revision="source-v1",
        ),
    ),
    warnings=(),
)
DISPUTED = Chunk(
    record_id="claim-webhook-timeout",
    revision="rev-disputed-2",
    text="The synthetic webhook timeout is ten seconds.",
    claim_status="disputed",
    origin_trust="submitted",
    verification_outcome="ambiguous",
    evidence=(
        EvidenceRef(
            artifact_id="artifact-timeout-log",
            digest="e" * 64,
            locator="logs/timeout.txt#bytes=0-32",
            source_revision="source-v7",
        ),
    ),
    warnings=("conflicting_evidence",),
)

# Operations that would promote, verify or decide. The provider must never call them.
PROMOTION_OPS = ("publish", "publish_lesson", "verify", "adjudicate", "respond_decision")


@dataclass
class FakeGateway:
    """Records every call; any non-port attribute access counts as a promotion attempt."""

    orient_results: list[MemoryResult | MemoryApiError] = field(default_factory=list)
    search_results: list[MemoryResult | MemoryApiError] = field(default_factory=list)
    calls: list[tuple[Any, ...]] = field(default_factory=list)
    candidates: list[CandidateDraft] = field(default_factory=list)
    unexpected: list[str] = field(default_factory=list)

    @property
    def publication_calls(self) -> list[str]:
        return [name for name in self.unexpected if name in PROMOTION_OPS] + [
            c[0] for c in self.calls if c[0] in PROMOTION_OPS
        ]

    def __getattr__(self, name: str) -> Any:
        if name.startswith("_"):
            raise AttributeError(name)
        self.unexpected.append(name)
        raise AttributeError(name)

    @staticmethod
    def _next(queue: list[MemoryResult | MemoryApiError], default: MemoryResult) -> MemoryResult:
        item = queue.pop(0) if queue else default
        if isinstance(item, MemoryApiError):
            raise item
        return item

    def orient(self, scope: Scope, query: str, budget_bytes: int) -> MemoryResult:
        self.calls.append(("orient", scope, query, budget_bytes))
        return self._next(self.orient_results, result_of(VERIFIED, DISPUTED))

    def search(self, scope: Scope, query: str, budget_bytes: int) -> MemoryResult:
        self.calls.append(("search", scope, query, budget_bytes))
        return self._next(self.search_results, result_of(VERIFIED, DISPUTED))

    def fetch(self, record_id: str, locator: str, budget_bytes: int) -> MemoryResult:
        self.calls.append(("fetch", record_id, locator, budget_bytes))
        return result_of()

    def submit_candidate(self, draft: CandidateDraft) -> CandidateReceipt:
        self.calls.append(("submit_candidate", draft))
        self.candidates.append(draft)
        return CandidateReceipt(
            claim_id=draft.claim_id,
            revision="rev-candidate-" + str(len(self.candidates)),
            claim_status="candidate",
            origin_trust="submitted",
        )


@dataclass
class FakeCapture:
    batches: list[list[Event]] = field(default_factory=list)

    def submit(self, events: list[Event]) -> Receipt:
        self.batches.append(list(events))
        return Receipt(ids=tuple(e.id for e in events), states=tuple("queued" for _ in events))


def result_of(*chunks: Chunk, size: int = 900) -> MemoryResult:
    return MemoryResult(chunks=tuple(chunks), bytes=size if chunks else 120, no_results=not chunks)


def error(code: ErrorCode) -> MemoryApiError:
    return MemoryApiError(code)


@pytest.fixture
def credential_file(tmp_path: Path) -> Path:
    path = tmp_path / "kb-agent.bearer"
    path.write_text(BEARER + "\n", encoding="ascii")
    os.chmod(path, 0o600)
    return path


@pytest.fixture
def env(credential_file: Path) -> dict[str, str]:
    return {
        "KB_MEMORY_ENDPOINT": "https://memory.example.com",
        "KB_MEMORY_PROJECT": PROJECT,
        "KB_MEMORY_CREDENTIAL_FILE": str(credential_file),
    }


@dataclass
class Harness:
    provider: KbMemoryProvider
    gateway: FakeGateway
    capture: FakeCapture


def make_provider(
    env: dict[str, str],
    tmp_path: Path,
    *,
    gateway: FakeGateway | None = None,
    capture: FakeCapture | None = None,
    session_id: str = "session-1",
    agent_context: str = "primary",
) -> Harness:
    gw = gateway or FakeGateway()
    cap = capture or FakeCapture()

    def gateway_factory(settings: Settings, bearer: str) -> FakeGateway:
        assert bearer == BEARER
        return gw

    def capture_factory(settings: Settings) -> FakeCapture:
        return cap

    provider = KbMemoryProvider(
        env=env, gateway_factory=gateway_factory, capture_factory=capture_factory
    )
    provider.initialize(
        session_id, hermes_home=str(tmp_path), platform="cli", agent_context=agent_context
    )
    return Harness(provider=provider, gateway=gw, capture=cap)
