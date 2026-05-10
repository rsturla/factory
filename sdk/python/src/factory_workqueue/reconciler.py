"""Reconciler SDK: response builders, handler, and types."""

from __future__ import annotations

import json
import logging
from dataclasses import dataclass, field
from datetime import timedelta
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Any, Callable

from ._duration import format_duration

logger = logging.getLogger(__name__)

ACTION_COMPLETED = "completed"
ACTION_CONVERGED = "converged"
ACTION_REQUEUE = "requeue"
ACTION_FAN_OUT = "fan_out"


@dataclass
class ProcessRequest:
    key: str
    attempt: int = 0
    priority: int = 0
    trace_id: str = ""

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> ProcessRequest:
        return cls(
            key=d["key"],
            attempt=d.get("attempt", 0),
            priority=d.get("priority", 0),
            trace_id=d.get("trace_id", ""),
        )


@dataclass
class ProcessResponse:
    action: str = ""
    requeue_after: str = ""
    fan_out_keys: list[str] = field(default_factory=list)
    error: str = ""

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {"action": self.action}
        if self.requeue_after:
            d["requeue_after"] = self.requeue_after
        if self.fan_out_keys:
            d["fan_out_keys"] = self.fan_out_keys
        if self.error:
            d["error"] = self.error
        return d

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> ProcessResponse:
        return cls(
            action=d.get("action", ""),
            requeue_after=d.get("requeue_after", ""),
            fan_out_keys=d.get("fan_out_keys", []),
            error=d.get("error", ""),
        )


ReconcileFunc = Callable[[ProcessRequest], ProcessResponse]


def completed() -> ProcessResponse:
    return ProcessResponse(action=ACTION_COMPLETED)


def converged() -> ProcessResponse:
    return ProcessResponse(action=ACTION_CONVERGED)


def requeue_after(delay: timedelta) -> ProcessResponse:
    return ProcessResponse(action=ACTION_REQUEUE, requeue_after=format_duration(delay))


def fan_out(*keys: str) -> ProcessResponse:
    if not keys:
        return ProcessResponse(action=ACTION_COMPLETED)
    return ProcessResponse(action=ACTION_FAN_OUT, fan_out_keys=list(keys))


class ReconcilerHandler:
    """ASGI application serving POST /process for reconciler callbacks."""

    def __init__(self, fn: ReconcileFunc) -> None:
        self._fn = fn

    async def __call__(
        self,
        scope: dict[str, Any],
        receive: Any,
        send: Any,
    ) -> None:
        if scope["type"] != "http":
            return

        method = scope.get("method", "GET")
        if method != "POST":
            await self._respond(send, 405, "method not allowed")
            return

        body = b""
        while True:
            msg = await receive()
            body += msg.get("body", b"")
            if not msg.get("more_body", False):
                break

        try:
            data = json.loads(body)
        except (json.JSONDecodeError, UnicodeDecodeError) as e:
            await self._respond(send, 400, f"invalid request body: {e}")
            return

        req = ProcessRequest.from_dict(data)

        try:
            resp = self._fn(req)
        except Exception as e:
            resp = ProcessResponse(error=str(e))

        resp_body = json.dumps(resp.to_dict()).encode()
        await self._send_json(send, 200, resp_body)

    async def _respond(self, send: Any, status: int, body: str) -> None:
        await self._send_json(send, status, body.encode())

    async def _send_json(self, send: Any, status: int, body: bytes) -> None:
        await send({
            "type": "http.response.start",
            "status": status,
            "headers": [
                [b"content-type", b"application/json"],
                [b"content-length", str(len(body)).encode()],
            ],
        })
        await send({"type": "http.response.body", "body": body})


def reconciler_http_handler(fn: ReconcileFunc) -> type[BaseHTTPRequestHandler]:
    """Return a stdlib BaseHTTPRequestHandler class that serves POST /process."""

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(length)

            try:
                data = json.loads(body)
            except (json.JSONDecodeError, UnicodeDecodeError) as e:
                self._reply(400, f"invalid request body: {e}")
                return

            req = ProcessRequest.from_dict(data)

            try:
                resp = fn(req)
            except Exception as e:
                resp = ProcessResponse(error=str(e))

            self._reply(200, json.dumps(resp.to_dict()))

        def _reply(self, status: int, body: str) -> None:
            encoded = body.encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)

        def log_message(self, format: str, *args: Any) -> None:
            logger.debug(format, *args)

    return Handler


def serve_http(fn: ReconcileFunc, host: str = "0.0.0.0", port: int = 8082) -> None:
    """Run reconciler with stdlib http.server. Zero external dependencies."""
    handler_cls = reconciler_http_handler(fn)
    server = HTTPServer((host, port), handler_cls)
    logger.info("serving reconciler on %s:%d", host, port)
    server.serve_forever()


def serve(fn: ReconcileFunc, host: str = "0.0.0.0", port: int = 8082) -> None:
    """Run reconciler handler with uvicorn (ASGI)."""
    import uvicorn

    app = ReconcilerHandler(fn)
    uvicorn.run(app, host=host, port=port)
