"""Qualify the kb provider inside a real Hermes Agent installation.

Usage (any interpreter matching the Hermes install, isolated mode):

    python -I qualify_hermes.py <this-package>/src <scratch-hermes-home> <hermes-site-packages>

Loads the provider through Hermes' own entry-point loader and drives its real
MemoryManager. Synthetic only: a loopback HTTP stub stands in for the kb API,
HERMES_HOME is a scratch directory, and no model, account or user data is used.
"""

import importlib.metadata as md
import json
import os
import site
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

SRC, HOME, SITE = sys.argv[1], sys.argv[2], sys.argv[3]
site.addsitedir(SITE)
sys.path.insert(0, SRC)
os.environ["HERMES_HOME"] = HOME

REF = {
    "artifact_id": "artifact-1",
    "digest": "d" * 64,
    "locator": "notes.md",
    "source_revision": "source-v1",
    "applicability": {},
    "origin": {"client_id": "collector", "trust": "submitted"},
}
CHUNK = {
    "record_id": "claim-1",
    "revision": "rev-verified-1",
    "text": "The synthetic billing service retries webhooks three times.",
    "claim_status": "verified",
    "origin_trust": "submitted",
    "verification_outcome": "supported",
    "warnings": [],
    "evidence": [REF],
}
BODY = json.dumps(
    {
        "schema_version": "3",
        "truncated": False,
        "visited": 1,
        "no_results": False,
        "chunks": [CHUNK],
    }
).encode()
PATHS: list[str] = []


class Stub(BaseHTTPRequestHandler):
    def do_POST(self) -> None:
        PATHS.append(self.path)
        self.rfile.read(int(self.headers["Content-Length"]))
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(BODY)))
        self.end_headers()
        self.wfile.write(BODY)

    def log_message(self, format: str, *args: object) -> None:
        return


def main() -> None:
    server = HTTPServer(("127.0.0.1", 0), Stub)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    bearer = os.path.join(HOME, "kb.bearer")
    with open(bearer, "w", encoding="ascii") as f:
        f.write("a" * 64)
    os.chmod(bearer, 0o600)
    os.environ.update(
        {
            "KB_MEMORY_ENDPOINT": f"http://127.0.0.1:{server.server_port}",
            "KB_MEMORY_PROJECT": "fixture-project",
            "KB_MEMORY_CREDENTIAL_FILE": bearer,
        }
    )
    from agent.memory_manager import MemoryManager  # type: ignore[import-not-found]
    from agent.memory_provider import MemoryProvider  # type: ignore[import-not-found]
    from plugins.memory import _load_provider_from_entry_point  # type: ignore[import-not-found]

    print("hermes_agent", md.version("hermes-agent"))
    ep = md.EntryPoint(
        name="kb_memory", value="kb_memory:register", group="hermes_agent.memory_providers"
    )
    provider = _load_provider_from_entry_point(ep, register_skills=False)
    print("loaded", type(provider).__name__, "subclass:", isinstance(provider, MemoryProvider))
    print("is_available", provider.is_available())
    manager = MemoryManager()
    manager.add_provider(provider)
    print("tools", sorted(manager.get_all_tool_names()))
    manager.initialize_all(
        "qual-session", hermes_home=HOME, platform="cli", agent_context="primary"
    )
    print("system_prompt_has_precedence", "the graph wins" in manager.build_system_prompt())
    recalled = manager.prefetch_all("webhook retries", session_id="qual-session")
    print(
        "prefetch_cites_revision",
        "[rev:rev-verified-1" in recalled,
        "bytes",
        len(recalled.encode()),
    )
    out = json.loads(manager.handle_tool_call("kb_memory_orient", {"query": "webhook"}))
    print("tool_evidence_revision_ids", out["evidence_revision_ids"])
    manager.on_memory_write("add", "memory", "synthetic note", metadata={})
    manager.sync_all("q", "a", session_id="qual-session")
    manager.flush_pending(5)
    manager.shutdown_all()
    print("api_paths", sorted(set(PATHS)))


if __name__ == "__main__":
    main()
