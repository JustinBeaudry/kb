"""Internal domain types (dataclasses) and boundary models (Pydantic).

Dataclasses are the provider's internal vocabulary. Pydantic models exist only
at the two I/O boundaries: the kb schema-v3 HTTP wire and Hermes tool arguments.
Claim status, origin trust and verification outcome stay separate fields; a
missing verification outcome means unverified, never supported.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field

ClaimStatus = Literal["candidate", "verified", "superseded", "disputed", "invalidated", "rejected"]
OriginTrust = Literal["submitted", "collector_attested", "gateway_observed", "owner_authenticated"]
VerificationOutcome = Literal["supported", "unsupported", "ambiguous"]

ORIENT_MAX_BYTES = 4096
SEARCH_MAX_BYTES = 16384
FETCH_MAX_BYTES = 16384


class ErrorCode(StrEnum):
    """Closed problem codes from the kb API plus local configuration failures."""

    INVALID_INPUT = "invalid_input"
    UNAUTHORIZED = "unauthorized"
    FORBIDDEN = "forbidden"
    STALE = "stale"
    CONFLICT = "conflict"
    DELETED = "deleted"
    INCOMPLETE = "incomplete"
    BUDGET = "budget"
    UNAVAILABLE = "unavailable"


class MemoryApiError(Exception):
    """Sentinel error returned by gateways; carries only a closed code, never remote text."""

    def __init__(self, code: ErrorCode) -> None:
        super().__init__(code.value)
        self.code = code


# -- Internal domain (dataclasses) -------------------------------------------------


@dataclass(frozen=True)
class Scope:
    project_id: str
    task_id: str | None = None
    session_id: str | None = None


@dataclass(frozen=True)
class EvidenceRef:
    artifact_id: str
    digest: str
    locator: str
    source_revision: str
    origin_trust: OriginTrust = "submitted"


@dataclass(frozen=True)
class Chunk:
    record_id: str
    revision: str
    text: str
    claim_status: ClaimStatus
    origin_trust: OriginTrust
    verification_outcome: VerificationOutcome | None
    evidence: tuple[EvidenceRef, ...]
    warnings: tuple[str, ...]


@dataclass(frozen=True)
class MemoryResult:
    """One bounded retrieval. ``bytes`` is the complete HTTP JSON body size."""

    chunks: tuple[Chunk, ...]
    bytes: int
    truncated: bool = False
    no_results: bool = False
    error: ErrorCode | None = None

    @property
    def evidence_revision_ids(self) -> tuple[str, ...]:
        """Immutable graph revisions an answer built from this result cites."""
        return tuple(c.revision for c in self.chunks)

    @property
    def source_revisions(self) -> tuple[str, ...]:
        return tuple(ref.source_revision for c in self.chunks for ref in c.evidence)


EMPTY_RESULT = MemoryResult(chunks=(), bytes=0, no_results=True)

EventKind = Literal["message", "tool_call", "tool_result", "checkpoint", "artifact"]


@dataclass(frozen=True)
class Event:
    """A conversation observation handed to the local C1 collector (never uploaded directly)."""

    id: str
    idempotency_key: str
    scope: Scope
    kind: EventKind
    observed_at: str
    sequence: int
    source_locator: str
    source_path: str
    content: dict[str, object]


@dataclass(frozen=True)
class Receipt:
    ids: tuple[str, ...]
    states: tuple[str, ...]


@dataclass(frozen=True)
class CandidateDraft:
    claim_id: str
    text: str
    kind: str
    scope: Scope
    evidence: tuple[EvidenceRef, ...] = ()
    depends_on: tuple[str, ...] = ()


@dataclass(frozen=True)
class CandidateReceipt:
    claim_id: str
    revision: str
    claim_status: ClaimStatus
    origin_trust: OriginTrust


@dataclass
class Settings:
    """Non-secret provider configuration; the bearer is read from a private file."""

    endpoint: str
    project_id: str
    credential_file: str
    capture_owner: Literal["provider", "external"] = "external"
    kb_bin: str = "kb"
    capture_profile: str = "default"
    prefetch_budget_bytes: int = ORIENT_MAX_BYTES


# -- HTTP wire boundary (Pydantic, schema v3) ---------------------------------------

_WIRE = ConfigDict(extra="forbid", frozen=True)


class WireOrigin(BaseModel):
    model_config = _WIRE
    client_id: str = Field(min_length=1)
    trust: OriginTrust
    attestation_id: str | None = None


class WireApplicability(BaseModel):
    model_config = _WIRE
    repository_revision: str | None = None
    dirty_digest: str | None = None
    environment: str | None = None
    versions: dict[str, str] | None = None
    valid_from: str | None = None
    valid_until: str | None = None


class WireEvidenceRef(BaseModel):
    model_config = _WIRE
    artifact_id: str = Field(min_length=1)
    digest: str = Field(min_length=1)
    locator: str
    source_revision: str = Field(min_length=1)
    origin: WireOrigin
    applicability: WireApplicability


class WirePassage(BaseModel):
    model_config = _WIRE
    start_byte: int = Field(ge=0)
    end_byte: int = Field(ge=0)
    source_bytes: int = Field(ge=0)


class WireEvidenceChunk(BaseModel):
    model_config = _WIRE
    record_id: str = Field(min_length=1)
    revision: str = Field(min_length=1)
    text: str
    claim_status: ClaimStatus
    origin_trust: OriginTrust
    verification_outcome: VerificationOutcome | None = None
    evidence: list[WireEvidenceRef]
    warnings: list[str]
    applicability: WireApplicability | None = None
    passage: WirePassage | None = None

    def to_domain(self) -> Chunk:
        return Chunk(
            record_id=self.record_id,
            revision=self.revision,
            text=self.text,
            claim_status=self.claim_status,
            origin_trust=self.origin_trust,
            verification_outcome=self.verification_outcome,
            evidence=tuple(
                EvidenceRef(
                    artifact_id=e.artifact_id,
                    digest=e.digest,
                    locator=e.locator,
                    source_revision=e.source_revision,
                    origin_trust=e.origin.trust,
                )
                for e in self.evidence
            ),
            warnings=tuple(self.warnings),
        )


class WireRetrievalResult(BaseModel):
    """Retrieval envelope. Task checkpoints are accepted but never treated as claims."""

    model_config = ConfigDict(extra="allow", frozen=True)
    schema_version: Literal["3"]
    chunks: list[WireEvidenceChunk]
    truncated: bool
    visited: int = Field(ge=0, le=50)
    no_results: bool
    continuation: str | None = None


class WireClaimRevision(BaseModel):
    model_config = ConfigDict(extra="allow", frozen=True)
    claim_id: str = Field(min_length=1)
    revision: str = Field(min_length=1, max_length=64)
    claim_status: ClaimStatus
    origin_trust: OriginTrust


class WireCandidateResult(BaseModel):
    model_config = _WIRE
    schema_version: Literal["3"]
    claim: WireClaimRevision


class WireProblem(BaseModel):
    """Only the closed ``code`` is read from a problem; messages are never relayed."""

    model_config = ConfigDict(extra="ignore", frozen=True)
    code: ErrorCode


class WireCaptureReceipt(BaseModel):
    """Output of ``kb capture enqueue`` (``{"ok":true,"value":{"id","state"}}``)."""

    model_config = ConfigDict(extra="ignore", frozen=True)
    ok: bool
    value: dict[str, str] | None = None


# -- Hermes tool-call boundary (Pydantic) --------------------------------------------


class ToolArgs(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)


class OrientArgs(ToolArgs):
    query: str = Field(default="", max_length=4096)
    budget_bytes: int = Field(default=ORIENT_MAX_BYTES, ge=1, le=ORIENT_MAX_BYTES)


class SearchArgs(ToolArgs):
    query: str = Field(min_length=1, max_length=4096)
    budget_bytes: int = Field(default=SEARCH_MAX_BYTES, ge=1, le=SEARCH_MAX_BYTES)


class FetchArgs(ToolArgs):
    record_id: str = Field(min_length=1, max_length=4096)
    locator: str = Field(min_length=1, max_length=4096)
    budget_bytes: int = Field(default=FETCH_MAX_BYTES, ge=1, le=FETCH_MAX_BYTES)


class EvidenceArg(ToolArgs):
    artifact_id: str = Field(min_length=1, max_length=4096)
    digest: str = Field(min_length=1, max_length=4096)
    locator: str = Field(min_length=1, max_length=4096)
    source_revision: str = Field(min_length=1, max_length=4096)


class SubmitCandidateArgs(ToolArgs):
    text: str = Field(min_length=1, max_length=16384)
    kind: Literal["finding", "correction", "hypothesis", "question"] = "finding"
    evidence: list[EvidenceArg] = Field(default_factory=list, max_length=50)
    depends_on: list[str] = Field(default_factory=list, max_length=50)
