"""Stand-in for ``agent.memory_provider.MemoryProvider`` from Hermes Agent 0.21.2.

Hermes is not a dependency of this package (0.21.2 is not published on PyPI;
the provider is installed into Hermes' own environment). Inside Hermes, the real
base class is imported instead (see ``provider.py``). Outside Hermes, this copy
keeps the provider importable and type-checkable. Method names and signatures
are copied from ``agent/memory_provider.py`` at 0.21.2, not invented; the
qualification test in ``tests/test_hermes_contract.py`` pins that list.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import Any

PINNED_HERMES_VERSION = "0.21.2"

# Every hook defined on MemoryProvider in Hermes Agent 0.21.2.
HERMES_0_21_2_HOOKS = (
    "name",
    "is_available",
    "initialize",
    "unavailable_reason",
    "system_prompt_block",
    "prefetch",
    "queue_prefetch",
    "recall_status",
    "sync_turn",
    "get_tool_schemas",
    "handle_tool_call",
    "shutdown",
    "on_turn_start",
    "identity_signature",
    "on_session_end",
    "on_session_switch",
    "on_pre_compress",
    "on_delegation",
    "get_config_schema",
    "save_config",
    "on_memory_write",
    "backup_paths",
)


class MemoryProvider(ABC):
    pre_compress_checkpoint_api_version = 1

    @property
    @abstractmethod
    def name(self) -> str: ...

    @abstractmethod
    def is_available(self) -> bool: ...

    @abstractmethod
    def initialize(self, session_id: str, **kwargs: Any) -> None: ...  # noqa: ANN401

    def unavailable_reason(self) -> str:
        return ""

    def system_prompt_block(self) -> str:
        return ""

    def prefetch(self, query: str, *, session_id: str = "") -> str:
        return ""

    def queue_prefetch(self, query: str, *, session_id: str = "") -> None:
        return None

    def recall_status(self) -> object | None:
        return None

    def sync_turn(
        self,
        user_content: str,
        assistant_content: str,
        *,
        session_id: str = "",
        messages: list[dict[str, Any]] | None = None,
        turn_author: dict[str, Any] | None = None,
    ) -> None:
        return None

    @abstractmethod
    def get_tool_schemas(self) -> list[dict[str, Any]]: ...

    def handle_tool_call(self, tool_name: str, args: dict[str, Any], **kwargs: Any) -> str:  # noqa: ANN401
        raise NotImplementedError(tool_name)

    def shutdown(self) -> None:
        return None

    def on_turn_start(self, turn_number: int, message: str, **kwargs: Any) -> None:  # noqa: ANN401
        return None

    def identity_signature(self) -> dict[str, Any]:
        return {}

    def on_session_end(self, messages: list[dict[str, Any]]) -> None:
        return None

    def on_session_switch(
        self,
        new_session_id: str,
        *,
        parent_session_id: str = "",
        reset: bool = False,
        rewound: bool = False,
        **kwargs: Any,  # noqa: ANN401
    ) -> None:
        return None

    def on_pre_compress(self, messages: list[dict[str, Any]]) -> str:
        return ""

    def on_delegation(
        self,
        task: str,
        result: str,
        *,
        child_session_id: str = "",
        **kwargs: Any,  # noqa: ANN401
    ) -> None:
        return None

    def get_config_schema(self) -> list[dict[str, Any]]:
        return []

    def save_config(self, values: dict[str, Any], hermes_home: str) -> None:
        return None

    def on_memory_write(
        self,
        action: str,
        target: str,
        content: str,
        metadata: dict[str, Any] | None = None,
    ) -> None:
        return None

    def backup_paths(self) -> list[str]:
        return []
