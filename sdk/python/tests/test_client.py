"""Tests for the kb Python client against a local HTTP server. Run with
python3 -I -m unittest discover -s sdk/python/tests (tests/sdk.test.ts does)."""
import json
import os
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from kb_client import KBClient, KBError, canonical_origin  # noqa: E402

BEARER = "kb_agent_test-bearer"


class Handler(BaseHTTPRequestHandler):
    seen: list = []
    reply = (200, {"schema_version": "3"}, "application/json")

    def _answer(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length) if length else b""
        Handler.seen.append({"method": self.command, "path": self.path, "headers": dict(self.headers), "body": body})
        status, payload, ctype = Handler.reply
        raw = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        if status in (301, 302):
            self.send_header("Location", "http://127.0.0.1:1/elsewhere")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    do_GET = do_POST = do_PUT = _answer

    def log_message(self, *args):
        pass


class ClientTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()
        cls.origin = f"http://127.0.0.1:{cls.server.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()

    def setUp(self):
        Handler.seen = []
        Handler.reply = (200, {"schema_version": "3"}, "application/json")
        self.kb = KBClient(self.origin, BEARER)

    def test_sends_bearer_json_and_an_encoded_path(self):
        self.kb.put_checkpoint("a/b?c", {"scope": {"project_id": "p", "task_id": "t"}, "objective": "x"})
        req = Handler.seen[0]
        self.assertEqual(req["method"], "PUT")
        self.assertEqual(req["path"], "/v1/checkpoints/a%2Fb%3Fc")
        self.assertEqual(req["headers"]["Authorization"], "Bearer " + BEARER)
        self.assertEqual(req["headers"]["Content-Type"], "application/json")
        self.assertEqual(json.loads(req["body"])["objective"], "x")

    def test_query_parameters_and_no_body_on_get(self):
        self.kb.fetch_evidence("art-1", {"locator": "line 1-2", "budget_bytes": 512, "project_id": None})
        req = Handler.seen[0]
        self.assertEqual(req["path"], "/v1/evidence/art-1?locator=line+1-2&budget_bytes=512")
        self.assertEqual(req["body"], b"")
        self.assertNotIn("Content-Type", req["headers"])

    def test_problem_becomes_kberror(self):
        Handler.reply = (403, {"code": "forbidden", "action": "Ask the owner.", "request_id": "r1"}, "application/problem+json")
        with self.assertRaises(KBError) as caught:
            self.kb.get_status()
        err = caught.exception
        self.assertEqual((err.status, err.code, err.action, err.request_id), (403, "forbidden", "Ask the owner.", "r1"))
        self.assertNotIn(BEARER, str(err))

    def test_redirect_is_refused_not_followed(self):
        Handler.reply = (302, b"", "text/plain")
        with self.assertRaises(KBError) as caught:
            self.kb.get_status()
        self.assertEqual(caught.exception.status, 302)
        self.assertEqual(len(Handler.seen), 1)

    def test_oversized_and_malformed_responses(self):
        Handler.reply = (200, {"long": "x" * 64}, "application/json")
        with self.assertRaises(KBError) as caught:
            KBClient(self.origin, BEARER, max_response_bytes=8).get_status()
        self.assertEqual(caught.exception.code, "oversized_response")
        Handler.reply = (200, b"not json", "application/json")
        with self.assertRaises(KBError) as caught:
            self.kb.get_status()
        self.assertEqual(caught.exception.code, "invalid_response")

    def test_network_failure(self):
        with self.assertRaises(KBError) as caught:
            KBClient("http://127.0.0.1:1", BEARER, timeout=2).get_status()
        self.assertEqual((caught.exception.status, caught.exception.code), (0, "network"))

    def test_origin_and_bearer_rules(self):
        for origin in ["http://memory.example.com", "https://memory.example.com/api", "https://u:p@memory.example.com",
                       "https://memory.example.com/?x=1", "http://localhost:8080", "ftp://x.example", "https://x.example:99999"]:
            self.assertIsNone(canonical_origin(origin), origin)
            with self.assertRaises(KBError):
                KBClient(origin, BEARER)
        self.assertEqual(canonical_origin("https://memory.example.com/"), "https://memory.example.com")
        self.assertEqual(canonical_origin("http://[::1]:8080"), "http://[::1]:8080")
        with self.assertRaises(KBError):
            KBClient("https://memory.example.com", "has space")

    def test_empty_id_is_refused_before_any_request(self):
        with self.assertRaises(KBError) as caught:
            self.kb.complete_artifact("", {})
        self.assertEqual(caught.exception.code, "invalid_input")
        self.assertEqual(Handler.seen, [])


if __name__ == "__main__":
    unittest.main()
