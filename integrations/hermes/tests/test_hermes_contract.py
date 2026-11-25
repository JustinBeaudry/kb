"""Pinned Hermes Agent 0.21.2 contract and provider configuration."""

from __future__ import annotations

import inspect
import json
import os
from pathlib import Path
from typing import Any

import pytest

import kb_memory
from kb_memory import hermes_contract
from kb_memory.provider import (
    TOOL_FETCH,
    TOOL_ORIENT,
    TOOL_SEARCH,
    TOOL_SUBMIT,
    KbMemoryProvider,
    read_bearer,
)

from .conftest import BEARER, make_provider

# Subset of Hermes 0.21.2 toolsets._HERMES_CORE_TOOLS; MemoryManager rejects shadowing names.
HERMES_RESERVED = {
    "memory",
    "session_search",
    "clarify",
    "delegate_task",
    "todo_list",
    "execute_code",
}


def test_contract_copy_defines_exactly_the_pinned_hooks() -> None:
    defined = {
        name
        for name, _ in inspect.getmembers(hermes_contract.MemoryProvider)
        if not name.startswith("_") and name != "pre_compress_checkpoint_api_version"
    }

    assert defined == set(hermes_contract.HERMES_0_21_2_HOOKS)
    assert hermes_contract.PINNED_HERMES_VERSION == "0.21.2"


def test_provider_overrides_only_pinned_hooks() -> None:
    overridden = {name for name in vars(KbMemoryProvider) if not name.startswith("_")}

    assert overridden <= set(hermes_contract.HERMES_0_21_2_HOOKS)
    assert issubclass(KbMemoryProvider, hermes_contract.MemoryProvider)


def test_register_hands_hermes_one_provider() -> None:
    class Ctx:
        def __init__(self) -> None:
            self.providers: list[Any] = []

        def register_memory_provider(self, provider: Any) -> None:
            self.providers.append(provider)

    ctx = Ctx()

    kb_memory.register(ctx)

    assert [p.name for p in ctx.providers] == ["kb_memory"]


def test_tools_do_not_shadow_hermes_core_tools(env: dict[str, str], tmp_path: Path) -> None:
    names = {s["name"] for s in make_provider(env, tmp_path).provider.get_tool_schemas()}

    assert names == {TOOL_ORIENT, TOOL_SEARCH, TOOL_FETCH, TOOL_SUBMIT}
    assert not names & HERMES_RESERVED


def test_config_schema_is_env_only_and_never_asks_for_the_secret(
    env: dict[str, str], tmp_path: Path
) -> None:
    schema = make_provider(env, tmp_path).provider.get_config_schema()

    assert all("env_var" in field for field in schema)
    assert not any(field.get("secret") for field in schema)


@pytest.mark.parametrize(
    ("override", "available"),
    [
        ({}, True),
        ({"KB_MEMORY_ENDPOINT": "http://memory.example.com"}, False),
        ({"KB_MEMORY_PROJECT": ""}, False),
        ({"KB_MEMORY_CAPTURE_OWNER": "both"}, False),
        ({"KB_MEMORY_PREFETCH_BUDGET": "5000"}, False),
        ({"KB_MEMORY_PREFETCH_BUDGET": "lots"}, False),
    ],
)
def test_availability_needs_valid_configuration(
    env: dict[str, str], override: dict[str, str], available: bool
) -> None:
    provider = KbMemoryProvider(env={**env, **override})

    assert provider.is_available() is available
    assert bool(provider.unavailable_reason()) is not available


@pytest.mark.parametrize(
    ("mode", "content", "accepted"),
    [(0o600, BEARER, True), (0o644, BEARER, False), (0o600, "nothex", False)],
)
def test_bearer_file_must_be_private(
    tmp_path: Path, mode: int, content: str, accepted: bool
) -> None:
    path = tmp_path / "bearer"
    path.write_text(content, encoding="ascii")
    os.chmod(path, mode)

    assert (read_bearer(str(path)) == BEARER) is accepted


def test_unconfigured_provider_is_inert() -> None:
    provider = KbMemoryProvider(env={})
    provider.initialize("s", hermes_home="/nonexistent")

    outcome = json.loads(provider.handle_tool_call(TOOL_ORIENT, {}))

    assert outcome == {"ok": False, "error": "unavailable"}
    assert provider.prefetch("q") == ""
    assert provider.system_prompt_block() == ""


@pytest.mark.parametrize(
    ("tool", "args"),
    [
        (TOOL_ORIENT, {"budget_bytes": 99999}),
        (TOOL_SEARCH, {}),
        (TOOL_FETCH, {"record_id": "a"}),
        (TOOL_SUBMIT, {"text": ""}),
        (TOOL_ORIENT, {"extra": 1}),
    ],
)
def test_invalid_tool_arguments_are_rejected(
    env: dict[str, str], tmp_path: Path, tool: str, args: dict[str, Any]
) -> None:
    h = make_provider(env, tmp_path)

    outcome = json.loads(h.provider.handle_tool_call(tool, args))

    assert outcome == {"ok": False, "error": "invalid_input"}
    assert h.gateway.calls == []


def test_fetch_tool_and_unknown_tool(env: dict[str, str], tmp_path: Path) -> None:
    h = make_provider(env, tmp_path)

    fetched = json.loads(
        h.provider.handle_tool_call(TOOL_FETCH, {"record_id": "artifact-1", "locator": "x"})
    )
    unknown = json.loads(h.provider.handle_tool_call("kb_memory_publish", {}))

    assert fetched["ok"] is True
    assert unknown == {"ok": False, "error": "unknown_tool"}
    assert h.gateway.publication_calls == []


def test_session_switch_rebinds_scope(env: dict[str, str], tmp_path: Path) -> None:
    h = make_provider(env, tmp_path)
    h.provider.on_session_switch("session-2", parent_session_id="session-1", reset=True)

    h.provider.handle_tool_call(TOOL_ORIENT, {"query": "q"})

    assert h.gateway.calls[0][1].session_id == "session-2"
