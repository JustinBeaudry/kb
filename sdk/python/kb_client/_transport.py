"""Transport for the generated kb client. Standard library only."""
from __future__ import annotations

import json
import re
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any

_BEARER = re.compile(r"[\x21-\x7e]{1,4096}")


class KBError(Exception):
    """A refused or failed request. code is kb's problem code when kb sent one.

    status is 0 when kb never answered (code "network" or "timeout")."""

    def __init__(self, status: int, code: str, *, action: str | None = None, field: str | None = None,
                 request_id: str | None = None, retry_after_seconds: int | None = None) -> None:
        super().__init__(f"kb request failed: {code}" if status == 0 else f"kb answered {status} {code}")
        self.status = status
        self.code = code
        self.action = action
        self.field = field
        self.request_id = request_id
        self.retry_after_seconds = retry_after_seconds


def canonical_origin(value: str) -> str | None:
    """The origin kb is reached at, or None when the client may not send a bearer there.

    https is accepted, and plain http only on 127.0.0.1 or [::1]."""
    try:
        u = urllib.parse.urlsplit(value)
        port = u.port
    except ValueError:
        return None
    if u.username or u.password or u.query or u.fragment or u.path not in ("", "/") or not u.hostname:
        return None
    host = u.hostname
    if u.scheme == "https":
        pass
    elif u.scheme == "http" and host in ("127.0.0.1", "::1"):
        pass
    else:
        return None
    netloc = f"[{host}]" if ":" in host else host
    if port is not None and not (u.scheme == "https" and port == 443) and not (u.scheme == "http" and port == 80):
        netloc += f":{port}"
    return f"{u.scheme}://{netloc}"


def _segment(value: str) -> str:
    """Encodes one path segment, so an ID can never change the path it is sent to."""
    if not isinstance(value, str) or value == "":
        raise KBError(0, "invalid_input")
    return urllib.parse.quote(value, safe="")


@dataclass(frozen=True)
class KBClientOptions:
    origin: str
    bearer: str
    timeout: float = 30.0
    max_response_bytes: int = 16 << 20


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):  # noqa: ANN001
        # Refused rather than followed, so the bearer only ever goes to the configured origin.
        return None


class Transport:
    def __init__(self, options: KBClientOptions) -> None:
        origin = canonical_origin(options.origin)
        if origin is None:
            raise KBError(0, "invalid_origin")
        if not isinstance(options.bearer, str) or not _BEARER.fullmatch(options.bearer):
            raise KBError(0, "invalid_bearer")
        self._origin = origin
        self._options = options
        self._opener = urllib.request.build_opener(_NoRedirect)

    def request(self, method: str, path: str, body: Any, query: dict[str, Any] | None) -> Any:
        url = self._origin + path
        params = {k: (str(v).lower() if isinstance(v, bool) else str(v)) for k, v in (query or {}).items() if v is not None}
        if params:
            url += "?" + urllib.parse.urlencode(params)
        headers = {"Authorization": f"Bearer {self._options.bearer}", "Accept": "application/json"}
        data = None
        if body is not None:
            headers["Content-Type"] = "application/json"
            data = json.dumps(body, ensure_ascii=False).encode("utf-8")
        req = urllib.request.Request(url, data=data, method=method, headers=headers)
        try:
            with self._opener.open(req, timeout=self._options.timeout) as resp:
                return self._decode(resp.status, resp.read(self._options.max_response_bytes + 1), ok=True)
        except urllib.error.HTTPError as err:
            raw = err.read(self._options.max_response_bytes + 1)
            raise _problem(err.code, self._decode(err.code, raw, ok=False)) from None
        except TimeoutError:
            raise KBError(0, "timeout") from None
        except urllib.error.URLError as err:
            raise KBError(0, "timeout" if isinstance(err.reason, TimeoutError) else "network") from None
        except OSError:
            raise KBError(0, "network") from None

    def _decode(self, status: int, raw: bytes, *, ok: bool) -> Any:
        if len(raw) > self._options.max_response_bytes:
            raise KBError(status, "oversized_response")
        if not raw:
            return None
        try:
            return json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, ValueError):
            if ok:
                raise KBError(status, "invalid_response") from None
            return None


def _problem(status: int, value: Any) -> KBError:
    p = value if isinstance(value, dict) else {}
    text = lambda k: p[k] if isinstance(p.get(k), str) else None  # noqa: E731
    retry = p.get("retry_after_seconds")
    return KBError(status, text("code") or f"http_{status}", action=text("action"), field=text("field"),
                   request_id=text("request_id"), retry_after_seconds=retry if isinstance(retry, int) else None)
