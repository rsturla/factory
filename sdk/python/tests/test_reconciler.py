import io
import json
from datetime import timedelta
from http.server import BaseHTTPRequestHandler
from unittest.mock import MagicMock

import pytest

from factory_workqueue.reconciler import (
    ACTION_COMPLETED,
    ACTION_CONVERGED,
    ACTION_FAN_OUT,
    ACTION_REQUEUE,
    ProcessRequest,
    ProcessResponse,
    ReconcilerHandler,
    completed,
    converged,
    fan_out,
    reconciler_http_handler,
    requeue_after,
)


class TestResponseBuilders:
    def test_completed(self):
        r = completed()
        assert r.action == ACTION_COMPLETED
        d = r.to_dict()
        assert d == {"action": "completed"}

    def test_converged(self):
        r = converged()
        assert r.action == ACTION_CONVERGED

    def test_requeue_after(self):
        r = requeue_after(timedelta(seconds=30))
        assert r.action == ACTION_REQUEUE
        assert r.requeue_after == "30s"

    def test_requeue_after_5m(self):
        r = requeue_after(timedelta(minutes=5))
        assert r.requeue_after == "5m0s"

    def test_fan_out(self):
        r = fan_out("a", "b", "c")
        assert r.action == ACTION_FAN_OUT
        assert r.fan_out_keys == ["a", "b", "c"]

    def test_fan_out_empty_returns_completed(self):
        r = fan_out()
        assert r.action == ACTION_COMPLETED
        assert r.fan_out_keys == []


class TestProcessRequest:
    def test_from_dict(self):
        d = {"key": "curl-1.0", "attempt": 2, "priority": 5, "trace_id": "abc"}
        req = ProcessRequest.from_dict(d)
        assert req.key == "curl-1.0"
        assert req.attempt == 2
        assert req.priority == 5
        assert req.trace_id == "abc"

    def test_from_dict_minimal(self):
        req = ProcessRequest.from_dict({"key": "k"})
        assert req.key == "k"
        assert req.attempt == 0
        assert req.priority == 0
        assert req.trace_id == ""


class TestProcessResponse:
    def test_to_dict_omits_empty(self):
        r = ProcessResponse(action="completed")
        d = r.to_dict()
        assert "requeue_after" not in d
        assert "fan_out_keys" not in d
        assert "error" not in d

    def test_to_dict_with_error(self):
        r = ProcessResponse(error="oops")
        d = r.to_dict()
        assert d["error"] == "oops"

    def test_roundtrip(self):
        r = ProcessResponse(action="fan_out", fan_out_keys=["x", "y"])
        d = r.to_dict()
        restored = ProcessResponse.from_dict(d)
        assert restored.action == r.action
        assert restored.fan_out_keys == r.fan_out_keys


# --- ASGI handler tests ---

async def _call_handler(handler, method, body):
    """Helper to invoke ASGI handler and capture response."""
    scope = {"type": "http", "method": method, "path": "/process"}
    received_status = None
    received_body = b""

    async def receive():
        return {"body": body, "more_body": False}

    async def send(msg):
        nonlocal received_status, received_body
        if msg["type"] == "http.response.start":
            received_status = msg["status"]
        elif msg["type"] == "http.response.body":
            received_body = msg.get("body", b"")

    await handler(scope, receive, send)
    return received_status, received_body


class TestReconcilerHandler:
    @pytest.mark.asyncio
    async def test_completed(self):
        handler = ReconcilerHandler(lambda req: completed())
        body = json.dumps({"key": "test-key", "attempt": 1, "priority": 0}).encode()
        status, resp_body = await _call_handler(handler, "POST", body)
        assert status == 200
        resp = json.loads(resp_body)
        assert resp["action"] == "completed"

    @pytest.mark.asyncio
    async def test_method_not_allowed(self):
        handler = ReconcilerHandler(lambda req: completed())
        status, _ = await _call_handler(handler, "GET", b"")
        assert status == 405

    @pytest.mark.asyncio
    async def test_bad_json(self):
        handler = ReconcilerHandler(lambda req: completed())
        status, _ = await _call_handler(handler, "POST", b"not json")
        assert status == 400

    @pytest.mark.asyncio
    async def test_exception_returns_error_field(self):
        def fail(req):
            raise RuntimeError("connection refused")

        handler = ReconcilerHandler(fail)
        body = json.dumps({"key": "k"}).encode()
        status, resp_body = await _call_handler(handler, "POST", body)
        assert status == 200
        resp = json.loads(resp_body)
        assert resp["error"] == "connection refused"

    @pytest.mark.asyncio
    async def test_requeue_response(self):
        handler = ReconcilerHandler(
            lambda req: requeue_after(timedelta(minutes=5))
        )
        body = json.dumps({"key": "k"}).encode()
        status, resp_body = await _call_handler(handler, "POST", body)
        assert status == 200
        resp = json.loads(resp_body)
        assert resp["action"] == "requeue"
        assert resp["requeue_after"] == "5m0s"

    @pytest.mark.asyncio
    async def test_fan_out_response(self):
        handler = ReconcilerHandler(
            lambda req: fan_out("child-1", "child-2")
        )
        body = json.dumps({"key": "parent"}).encode()
        status, resp_body = await _call_handler(handler, "POST", body)
        assert status == 200
        resp = json.loads(resp_body)
        assert resp["action"] == "fan_out"
        assert resp["fan_out_keys"] == ["child-1", "child-2"]

    @pytest.mark.asyncio
    async def test_request_fields_passed(self):
        received = {}

        def capture(req):
            received["key"] = req.key
            received["attempt"] = req.attempt
            received["priority"] = req.priority
            received["trace_id"] = req.trace_id
            return completed()

        handler = ReconcilerHandler(capture)
        body = json.dumps({
            "key": "pkg-1.0",
            "attempt": 3,
            "priority": 10,
            "trace_id": "00-abc-def-01",
        }).encode()
        await _call_handler(handler, "POST", body)
        assert received["key"] == "pkg-1.0"
        assert received["attempt"] == 3
        assert received["priority"] == 10
        assert received["trace_id"] == "00-abc-def-01"


# --- stdlib HTTP handler tests ---

def _call_http_handler(handler_cls, method, body):
    """Invoke stdlib handler and capture response."""
    rfile = io.BytesIO(body)
    wfile = io.BytesIO()

    request_line = f"{method} /process HTTP/1.1\r\n"
    headers = f"Content-Type: application/json\r\nContent-Length: {len(body)}\r\n\r\n"

    environ = MagicMock()
    environ.makefile.return_value = rfile

    handler = handler_cls.__new__(handler_cls)
    handler.rfile = rfile
    handler.wfile = wfile
    handler.headers = {"Content-Type": "application/json", "Content-Length": str(len(body))}
    handler.requestline = request_line
    handler.command = method
    handler.request_version = "HTTP/1.1"
    handler.client_address = ("127.0.0.1", 0)
    handler.server = MagicMock()
    handler._headers_buffer = []
    handler.responses = BaseHTTPRequestHandler.responses

    handler.do_POST()
    return wfile.getvalue()


class TestReconcilerHttpHandler:
    def test_completed(self):
        handler_cls = reconciler_http_handler(lambda req: completed())
        body = json.dumps({"key": "test"}).encode()
        output = _call_http_handler(handler_cls, "POST", body)
        # Parse response body (after headers)
        parts = output.split(b"\r\n\r\n", 1)
        resp = json.loads(parts[1])
        assert resp["action"] == "completed"

    def test_exception_returns_error(self):
        def fail(req):
            raise RuntimeError("boom")

        handler_cls = reconciler_http_handler(fail)
        body = json.dumps({"key": "k"}).encode()
        output = _call_http_handler(handler_cls, "POST", body)
        parts = output.split(b"\r\n\r\n", 1)
        resp = json.loads(parts[1])
        assert resp["error"] == "boom"

    def test_returns_handler_class(self):
        handler_cls = reconciler_http_handler(lambda req: completed())
        assert issubclass(handler_cls, BaseHTTPRequestHandler)
