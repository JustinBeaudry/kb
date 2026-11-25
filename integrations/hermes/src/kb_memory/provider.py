"""Hermes Agent memory provider (handler layer) over kb graph memory.

Hook names and signatures are those of ``agent.memory_provider.MemoryProvider``
in Hermes Agent 0.21.2. This module parses Hermes inputs, delegates to
``ConversationMemoryService`` and shapes Hermes outputs; it holds no business
rules. Supported automatic hooks: ``prefetch``/``queue_prefetch`` (cited
recall), ``sync_turn`` (capture via the elected owner), ``on_memory_write``
(kept local), ``on_session_switch``/``on_session_end``. Checkpoints and status
are not provided here; use the C3 MCP tools (``kb-mcp``) for those.
"""

from __future__ import annotations

import os
import stat
from collections.abc import Callable
from pathlib import Path
from typing import TYPE_CHECKING, Any, Literal

from pydantic import ValidationError

from kb_memory.gateway import (
    CaptureGateway,
    CollectorCaptureGateway,
    HttpMemoryGateway,
    MemoryGateway,
    canonical_origin,
    valid_bearer,
)
from kb_memory.models import (
    ORIENT_MAX_BYTES,
    EvidenceRef,
    FetchArgs,
    OrientArgs,
    SearchArgs,
    Settings,
    SubmitCandidateArgs,
)
from kb_memory.service import CaptureElection, ConversationMemoryService, ToolOutcome

if TYPE_CHECKING:
    from kb_memory.hermes_contract import MemoryProvider as _Base
else:  # pragma: no cover - exercised only inside a Hermes process
    try:
        from agent.memory_provider import MemoryProvider as _Base
    except ImportError:
        from kb_memory.hermes_contract import MemoryProvider as _Base

PROVIDER_NAME = "kb_memory"
TOOL_ORIENT = "kb_memory_orient"
TOOL_SEARCH = "kb_memory_search"
TOOL_FETCH = "kb_memory_fetch"
TOOL_SUBMIT = "kb_memory_submit_candidate"

ENV_ENDPOINT = "KB_MEMORY_ENDPOINT"
ENV_PROJECT = "KB_MEMORY_PROJECT"
ENV_CREDENTIAL_FILE = "KB_MEMORY_CREDENTIAL_FILE"
ENV_CAPTURE_OWNER = "KB_MEMORY_CAPTURE_OWNER"
ENV_KB_BIN = "KB_MEMORY_KB_BIN"
ENV_CAPTURE_PROFILE = "KB_MEMORY_CAPTURE_PROFILE"
ENV_PREFETCH_BUDGET = "KB_MEMORY_PREFETCH_BUDGET"

GatewayFactory = Callable[[Settings, str], MemoryGateway]
CaptureFactory = Callable[[Settings], CaptureGateway]


def read_bearer(path: str) -> str | None:
    """Bearer from an owner-only (0600) regular file; never from env or argv."""
    try:
        info = os.stat(path, follow_symlinks=False)
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid():
            return None
        token = Path(path).read_text(encoding="ascii").strip()
    except (OSError, UnicodeDecodeError):
        return None
    return token if valid_bearer(token) else None


def load_settings(env: dict[str, str] | None = None) -> Settings | None:
    env = dict(os.environ) if env is None else env
    endpoint = env.get(ENV_ENDPOINT, "")
    project = env.get(ENV_PROJECT, "")
    credential_file = env.get(ENV_CREDENTIAL_FILE, "")
    owner = env.get(ENV_CAPTURE_OWNER, "external")
    try:
        budget = int(env.get(ENV_PREFETCH_BUDGET, str(ORIENT_MAX_BYTES)))
    except ValueError:
        return None
    if (
        canonical_origin(endpoint) is None
        or not project.strip()
        or not credential_file
        or owner not in ("provider", "external")
        or not 1 <= budget <= ORIENT_MAX_BYTES
    ):
        return None
    capture_owner: Literal["provider", "external"] = (
        "provider" if owner == "provider" else "external"
    )
    return Settings(
        endpoint=endpoint,
        project_id=project,
        credential_file=credential_file,
        capture_owner=capture_owner,
        kb_bin=env.get(ENV_KB_BIN, "kb"),
        capture_profile=env.get(ENV_CAPTURE_PROFILE, "default"),
        prefetch_budget_bytes=budget,
    )


def _http_gateway(settings: Settings, bearer: str) -> MemoryGateway:
    return HttpMemoryGateway(settings.endpoint, bearer)


def _collector(settings: Settings) -> CaptureGateway:
    return CollectorCaptureGateway(settings.kb_bin, settings.endpoint, settings.capture_profile)


def _schema(
    name: str, description: str, properties: dict[str, Any], required: list[str]
) -> dict[str, Any]:
    return {
        "name": name,
        "description": description,
        "parameters": {"type": "object", "properties": properties, "required": required},
    }


_BUDGET = {"type": "integer", "minimum": 1}
_EVIDENCE = {
    "type": "object",
    "properties": {
        k: {"type": "string"} for k in ("artifact_id", "digest", "locator", "source_revision")
    },
    "required": ["artifact_id", "digest", "locator", "source_revision"],
}

TOOL_SCHEMAS: list[dict[str, Any]] = [
    _schema(
        TOOL_ORIENT,
        "Bounded orientation over verified kb graph memory (max 4096 bytes). Returns cited "
        "claims with claim_status, verification_outcome and evidence revisions.",
        {"query": {"type": "string"}, "budget_bytes": {**_BUDGET, "maximum": 4096}},
        [],
    ),
    _schema(
        TOOL_SEARCH,
        "Search published kb graph memory with evidence (max 16384 bytes). Cite revision IDs.",
        {"query": {"type": "string"}, "budget_bytes": {**_BUDGET, "maximum": 16384}},
        ["query"],
    ),
    _schema(
        TOOL_FETCH,
        "Fetch the source passage behind a cited evidence reference. A passage is source "
        "evidence, not a verified claim.",
        {
            "record_id": {"type": "string", "description": "evidence artifact_id"},
            "locator": {"type": "string"},
            "budget_bytes": {**_BUDGET, "maximum": 16384},
        },
        ["record_id", "locator"],
    ),
    _schema(
        TOOL_SUBMIT,
        "Propose a finding or correction as an UNVERIFIED candidate. It never changes verified "
        "memory; repeating recalled memory is not new evidence and is not submitted.",
        {
            "text": {"type": "string"},
            "kind": {"type": "string", "enum": ["finding", "correction", "hypothesis", "question"]},
            "evidence": {"type": "array", "items": _EVIDENCE, "maxItems": 50},
            "depends_on": {"type": "array", "items": {"type": "string"}, "maxItems": 50},
        },
        ["text"],
    ),
]

SYSTEM_PROMPT = (
    "kb graph memory is connected. It is the authoritative shared memory: recalled lines "
    "labeled [verified] carry claim revisions and evidence; cite their [rev:...] IDs. Lines "
    "labeled candidate or disputed are not established fact; present them as uncertain. Your "
    "built-in notes are private working state: when a note conflicts with a [verified] line, "
    "the graph wins; say so, and use kb_memory_submit_candidate to propose a correction, "
    "which only creates an unverified candidate."
)


class KbMemoryProvider(_Base):
    def __init__(
        self,
        *,
        env: dict[str, str] | None = None,
        gateway_factory: GatewayFactory = _http_gateway,
        capture_factory: CaptureFactory = _collector,
    ) -> None:
        self._env = env
        self._gateway_factory = gateway_factory
        self._capture_factory = capture_factory
        self._service: ConversationMemoryService | None = None
        self._session = ""
        self._reason = ""

    @property
    def name(self) -> str:
        return PROVIDER_NAME

    def _settings(self) -> Settings | None:
        return load_settings(self._env)

    def is_available(self) -> bool:
        settings = self._settings()
        if settings is None:
            self._reason = f"Set {ENV_ENDPOINT}, {ENV_PROJECT} and {ENV_CREDENTIAL_FILE}."
            return False
        if read_bearer(settings.credential_file) is None:
            self._reason = "The kb credential file must be an owner-only (0600) 64-hex bearer."
            return False
        return True

    def unavailable_reason(self) -> str:
        return self._reason

    def initialize(self, session_id: str, **kwargs: Any) -> None:  # noqa: ANN401
        settings = self._settings()
        bearer = read_bearer(settings.credential_file) if settings else None
        if settings is None or bearer is None:
            return
        capture: CaptureGateway | None = None
        election: CaptureElection | None = None
        # Only the primary agent context may own capture; subagents, cron and
        # flush contexts never upload, so one session is captured at most once.
        primary = kwargs.get("agent_context", "primary") == "primary"
        if settings.capture_owner == "provider" and primary:
            home = Path(str(kwargs.get("hermes_home") or os.environ.get("HERMES_HOME", ".")))
            capture = self._capture_factory(settings)
            election = CaptureElection(home / "kb_memory" / "capture-owner")
        self._service = ConversationMemoryService(
            self._gateway_factory(settings, bearer),
            settings.project_id,
            capture=capture,
            election=election,
            prefetch_budget_bytes=settings.prefetch_budget_bytes,
        )
        self._session = session_id

    def _sid(self, session_id: str) -> str:
        return session_id or self._session

    def system_prompt_block(self) -> str:
        return SYSTEM_PROMPT if self._service else ""

    def prefetch(self, query: str, *, session_id: str = "") -> str:
        if not self._service:
            return ""
        return self._service.prefetch(query, self._sid(session_id))

    def queue_prefetch(self, query: str, *, session_id: str = "") -> None:
        if self._service:
            self._service.queue_prefetch(query, self._sid(session_id))

    def sync_turn(
        self,
        user_content: str,
        assistant_content: str,
        *,
        session_id: str = "",
        messages: list[dict[str, Any]] | None = None,
        turn_author: dict[str, Any] | None = None,
    ) -> None:
        if self._service:
            self._service.capture_turn(self._sid(session_id), user_content, assistant_content)

    def get_tool_schemas(self) -> list[dict[str, Any]]:
        return [dict(s) for s in TOOL_SCHEMAS]

    def handle_tool_call(self, tool_name: str, args: dict[str, Any], **kwargs: Any) -> str:  # noqa: ANN401
        if not self._service:
            return ToolOutcome(ok=False, payload={"error": "unavailable"}).to_json()
        service, sid = self._service, self._session
        try:
            if tool_name == TOOL_ORIENT:
                o = OrientArgs.model_validate(args)
                return service.describe(service.recall(o.query, sid, o.budget_bytes)).to_json()
            if tool_name == TOOL_SEARCH:
                s = SearchArgs.model_validate(args)
                return service.describe(service.search(s.query, sid, s.budget_bytes)).to_json()
            if tool_name == TOOL_FETCH:
                f = FetchArgs.model_validate(args)
                result = service.fetch(f.record_id, f.locator, sid, f.budget_bytes)
                return service.describe(result).to_json()
            if tool_name == TOOL_SUBMIT:
                c = SubmitCandidateArgs.model_validate(args)
                evidence = tuple(
                    EvidenceRef(e.artifact_id, e.digest, e.locator, e.source_revision)
                    for e in c.evidence
                )
                return service.submit_candidate(
                    sid, c.text, c.kind, evidence, tuple(c.depends_on)
                ).to_json()
        except ValidationError:
            return ToolOutcome(ok=False, payload={"error": "invalid_input"}).to_json()
        return ToolOutcome(ok=False, payload={"error": "unknown_tool"}).to_json()

    def on_memory_write(
        self,
        action: str,
        target: str,
        content: str,
        metadata: dict[str, Any] | None = None,
    ) -> None:
        # Built-in notes stay Hermes-local working state; nothing is uploaded.
        if self._service:
            self._service.note_written()

    def on_session_switch(
        self,
        new_session_id: str,
        *,
        parent_session_id: str = "",
        reset: bool = False,
        rewound: bool = False,
        **kwargs: Any,  # noqa: ANN401
    ) -> None:
        if self._service and reset and self._session:
            self._service.end_session(self._session)
        self._session = new_session_id

    def on_session_end(self, messages: list[dict[str, Any]]) -> None:
        if self._service and self._session:
            self._service.end_session(self._session)

    def shutdown(self) -> None:
        self.on_session_end([])

    def get_config_schema(self) -> list[dict[str, Any]]:
        # Every field is env-only (Hermes 0.21.2 save_config contract); the bearer
        # itself is never prompted for, only the path of its private file.
        return [
            {
                "key": "endpoint",
                "description": "kb memory API origin",
                "required": True,
                "env_var": ENV_ENDPOINT,
            },
            {
                "key": "project",
                "description": "kb project ID",
                "required": True,
                "env_var": ENV_PROJECT,
            },
            {
                "key": "credential_file",
                "description": "Path to the 0600 agent bearer file",
                "required": True,
                "env_var": ENV_CREDENTIAL_FILE,
            },
            {
                "key": "capture_owner",
                "description": "Who captures turns",
                "choices": ["external", "provider"],
                "default": "external",
                "env_var": ENV_CAPTURE_OWNER,
            },
        ]


def register(ctx: Any) -> None:  # noqa: ANN401 - Hermes plugin context
    """Hermes entry point: ``ctx.register_memory_provider(...)``."""
    ctx.register_memory_provider(KbMemoryProvider())
