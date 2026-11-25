"""Behavior of the Hermes provider over a fake kb gateway (synthetic data only)."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from kb_memory.models import ErrorCode, Scope
from kb_memory.provider import SYSTEM_PROMPT, TOOL_ORIENT, TOOL_SEARCH, TOOL_SUBMIT

from .conftest import (
    DISPUTED,
    PROJECT,
    VERIFIED,
    FakeCapture,
    FakeGateway,
    error,
    make_provider,
    result_of,
)

EXPECTED_REVISIONS = [VERIFIED.revision, DISPUTED.revision]


@pytest.mark.parametrize(("tool", "operation"), [(TOOL_ORIENT, "orient"), (TOOL_SEARCH, "search")])
def test_answers_preserve_evidence_references(
    env: dict[str, str], tmp_path: Path, tool: str, operation: str
) -> None:
    h = make_provider(env, tmp_path)

    result = json.loads(h.provider.handle_tool_call(tool, {"query": "webhook retries"}))

    assert result["evidence_revision_ids"] == EXPECTED_REVISIONS
    assert [c["claim_status"] for c in result["chunks"]] == ["verified", "disputed"]
    assert [c["verification_outcome"] for c in result["chunks"]] == ["supported", "ambiguous"]
    assert result["chunks"][0]["evidence"] == [
        {
            "artifact_id": "artifact-billing-notes",
            "digest": "d" * 64,
            "locator": "billing/notes.md#bytes=0-64",
            "source_revision": "source-v1",
            "origin_trust": "submitted",
        }
    ]
    assert h.gateway.calls[0][0] == operation
    assert h.gateway.publication_calls == []


def test_recall_lines_cite_revisions_and_label_uncertainty(
    env: dict[str, str], tmp_path: Path
) -> None:
    h = make_provider(env, tmp_path)

    text = h.provider.prefetch("webhook retries", session_id="session-1")

    cited = f"[rev:{VERIFIED.revision}; evidence: artifact-billing-notes@source-v1"
    assert f"- [verified] {VERIFIED.text} {cited}" in text
    assert (
        f"[disputed: uncertain, present alternatives, do not state as fact] {DISPUTED.text}" in text
    )
    assert "warnings=conflicting_evidence" in text


@pytest.mark.parametrize(
    "echo",
    [
        VERIFIED.text,
        "the synthetic BILLING service retries webhooks three times!!",
        f"As recorded, {VERIFIED.text.lower()} That is settled.",
    ],
)
def test_assistant_echo_is_not_independent_evidence(
    env: dict[str, str], tmp_path: Path, echo: str
) -> None:
    h = make_provider(env, tmp_path)
    h.provider.prefetch("webhook retries", session_id="session-1")
    same_evidence = [
        {
            "artifact_id": "artifact-billing-notes",
            "digest": "d" * 64,
            "locator": "billing/notes.md#bytes=0-64",
            "source_revision": "source-v1",
        }
    ]

    result = json.loads(
        h.provider.handle_tool_call(TOOL_SUBMIT, {"text": echo, "evidence": same_evidence})
    )

    assert result == {
        "ok": True,
        "submitted": False,
        "reason": "echo_of_recalled_memory",
        "evidence_revision_ids": [VERIFIED.revision],
    }
    assert h.gateway.candidates == []
    assert h.gateway.publication_calls == []


def test_echo_with_new_evidence_is_submitted_as_dependent_candidate(
    env: dict[str, str], tmp_path: Path
) -> None:
    h = make_provider(env, tmp_path)
    h.provider.prefetch("webhook retries", session_id="session-1")
    new_evidence = [
        {
            "artifact_id": "artifact-new-test",
            "digest": "f" * 64,
            "locator": "t#1",
            "source_revision": "s9",
        }
    ]

    result = json.loads(
        h.provider.handle_tool_call(TOOL_SUBMIT, {"text": VERIFIED.text, "evidence": new_evidence})
    )

    assert result["submitted"] is True
    assert result["claim_status"] == "candidate"
    assert h.gateway.candidates[0].depends_on == (VERIFIED.revision,)
    assert [e.artifact_id for e in h.gateway.candidates[0].evidence] == ["artifact-new-test"]


def test_captured_answer_is_marked_derived_not_independent(
    env: dict[str, str], tmp_path: Path
) -> None:
    h = make_provider({**env, "KB_MEMORY_CAPTURE_OWNER": "provider"}, tmp_path)
    h.provider.prefetch("webhook retries", session_id="session-1")

    h.provider.sync_turn(
        "How many retries?", f"{VERIFIED.text} [rev:{VERIFIED.revision}]", session_id="session-1"
    )

    user, assistant = h.capture.batches[0]
    assert user.content == {"harness": "hermes", "role": "user", "text": "How many retries?"}
    assert assistant.content["independent_observation"] is False
    assert assistant.content["derived_from_revisions"] == [VERIFIED.revision]
    assert assistant.scope == Scope(project_id=PROJECT, session_id="session-1")
    assert (user.sequence, assistant.sequence) == (0, 1)


def test_builtin_note_cannot_override_graph(env: dict[str, str], tmp_path: Path) -> None:
    note = "The synthetic billing service retries webhooks five times."
    h = make_provider({**env, "KB_MEMORY_CAPTURE_OWNER": "provider"}, tmp_path)
    h.provider.on_memory_write("add", "memory", note, metadata={"write_origin": "tool"})

    recalled = h.provider.prefetch("webhook retries", session_id="session-1")

    assert f"[verified] {VERIFIED.text} [rev:{VERIFIED.revision}" in recalled
    assert note not in recalled
    assert h.gateway.candidates == []
    assert h.capture.batches == []
    assert h.gateway.publication_calls == []
    assert "the graph wins" in h.provider.system_prompt_block()
    assert h.provider.system_prompt_block() == SYSTEM_PROMPT


def test_note_correction_becomes_only_a_candidate(env: dict[str, str], tmp_path: Path) -> None:
    h = make_provider(env, tmp_path)

    result = json.loads(
        h.provider.handle_tool_call(
            TOOL_SUBMIT,
            {
                "text": "Billing may retry five times after the synthetic upgrade.",
                "kind": "correction",
            },
        )
    )

    assert result["claim_status"] == "candidate"
    assert h.gateway.candidates[0].scope == Scope(PROJECT, "hermes-session-1", "session-1")
    assert h.gateway.publication_calls == []


@pytest.mark.parametrize("code", [ErrorCode.FORBIDDEN, ErrorCode.UNAUTHORIZED, ErrorCode.DELETED])
def test_forbidden_source_is_not_retried_with_broader_scope(
    env: dict[str, str], tmp_path: Path, code: ErrorCode
) -> None:
    gateway = FakeGateway(orient_results=[error(code)])
    h = make_provider(env, tmp_path, gateway=gateway)

    result = json.loads(h.provider.handle_tool_call(TOOL_ORIENT, {"query": "restricted"}))

    assert result["ok"] is False
    assert result["error"] == code.value
    assert [c[:2] for c in gateway.calls] == [("orient", Scope(PROJECT, None, "session-1"))]


def test_transport_failure_retries_once_with_identical_scope(
    env: dict[str, str], tmp_path: Path
) -> None:
    gateway = FakeGateway(orient_results=[error(ErrorCode.UNAVAILABLE), result_of(VERIFIED)])
    h = make_provider(env, tmp_path, gateway=gateway)

    result = json.loads(h.provider.handle_tool_call(TOOL_ORIENT, {"query": "retries"}))

    assert result["evidence_revision_ids"] == [VERIFIED.revision]
    assert [c[1] for c in gateway.calls] == [Scope(PROJECT, None, "session-1")] * 2


@pytest.mark.parametrize("requested_budget", [256, 512, 4096])
def test_background_prefetch_respects_budget(
    env: dict[str, str], tmp_path: Path, requested_budget: int
) -> None:
    many = [VERIFIED, DISPUTED] * 20
    gateway = FakeGateway(orient_results=[result_of(*many, size=requested_budget)])
    h = make_provider(
        {**env, "KB_MEMORY_PREFETCH_BUDGET": str(requested_budget)}, tmp_path, gateway=gateway
    )
    h.provider.queue_prefetch("webhook retries", session_id="session-1")

    injected = h.provider.prefetch("webhook retries", session_id="session-1")

    assert [c[0] for c in gateway.calls] == ["orient"]  # queued result consumed, not refetched
    assert gateway.calls[0][3] <= requested_budget
    assert len(injected.encode()) <= requested_budget


def test_over_budget_response_is_discarded(env: dict[str, str], tmp_path: Path) -> None:
    requested_budget = 512
    gateway = FakeGateway(orient_results=[result_of(VERIFIED, size=requested_budget + 1)])
    h = make_provider(
        {**env, "KB_MEMORY_PREFETCH_BUDGET": str(requested_budget)}, tmp_path, gateway=gateway
    )
    assert h.provider._service is not None

    result = h.provider._service.recall("q", "session-1")

    assert result.bytes <= requested_budget
    assert result.error == ErrorCode.BUDGET
    assert result.chunks == ()


@pytest.mark.parametrize(
    ("owner", "context", "expected_batches"),
    [
        ("provider", "primary", 1),
        ("external", "primary", 0),
        ("provider", "subagent", 0),
        ("provider", "cron", 0),
    ],
)
def test_only_elected_primary_owner_captures(
    env: dict[str, str], tmp_path: Path, owner: str, context: str, expected_batches: int
) -> None:
    h = make_provider({**env, "KB_MEMORY_CAPTURE_OWNER": owner}, tmp_path, agent_context=context)

    h.provider.sync_turn("hi there", "hello", session_id="session-1")

    assert len(h.capture.batches) == expected_batches


def test_second_provider_on_same_session_does_not_double_upload(
    env: dict[str, str], tmp_path: Path
) -> None:
    owner_env = {**env, "KB_MEMORY_CAPTURE_OWNER": "provider"}
    first = make_provider(owner_env, tmp_path, capture=FakeCapture())
    second = make_provider(owner_env, tmp_path, capture=FakeCapture())
    first.provider.sync_turn("one", "two", session_id="session-1")

    second.provider.sync_turn("one", "two", session_id="session-1")

    assert (len(first.capture.batches), len(second.capture.batches)) == (1, 0)


def test_released_session_can_be_captured_by_next_owner(
    env: dict[str, str], tmp_path: Path
) -> None:
    owner_env = {**env, "KB_MEMORY_CAPTURE_OWNER": "provider"}
    first = make_provider(owner_env, tmp_path, capture=FakeCapture())
    second = make_provider(owner_env, tmp_path, capture=FakeCapture())
    first.provider.sync_turn("one", "two", session_id="session-1")
    first.provider.on_session_end([])

    second.provider.sync_turn("three", "four", session_id="session-1")

    assert len(second.capture.batches) == 1
