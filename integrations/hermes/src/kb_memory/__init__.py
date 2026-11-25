"""kb graph memory provider for Hermes Agent 0.21.2.

Hermes loads this package through the ``hermes_agent.memory_providers`` entry
point (or a ``$HERMES_HOME/plugins/kb_memory`` directory) and calls
``register(ctx)``, which calls ``ctx.register_memory_provider`` with a
``MemoryProvider`` subclass.
"""

from kb_memory.provider import KbMemoryProvider, register

__all__ = ["KbMemoryProvider", "register"]
