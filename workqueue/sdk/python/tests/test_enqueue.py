import json

import httpx
import pytest

from factory_workqueue.enqueue import AsyncEnqueueClient, EnqueueClient
from factory_workqueue.errors import APIError


def _mock_transport(handler):
    return httpx.MockTransport(handler)


def _make_client(handler, retries=0) -> EnqueueClient:
    return EnqueueClient(
        "http://test",
        client=httpx.Client(transport=_mock_transport(handler)),
        retries=retries,
    )


class TestEnqueueClient:
    def test_enqueue_success(self):
        def handler(req: httpx.Request):
            assert req.url.path == "/enqueue"
            assert req.method == "POST"
            body = json.loads(req.content)
            assert body["queue"] == "builds"
            assert body["key"] == "pkg-1.0"
            assert body["priority"] == 5
            return httpx.Response(200, json={"status": "enqueued"})

        with _make_client(handler) as c:
            c.enqueue("builds", "pkg-1.0", 5)

    def test_enqueue_201(self):
        def handler(req: httpx.Request):
            return httpx.Response(201, json={"status": "enqueued"})

        with _make_client(handler) as c:
            c.enqueue("q", "k")

    def test_enqueue_error(self):
        def handler(req: httpx.Request):
            return httpx.Response(500, text="internal error")

        with _make_client(handler) as c:
            with pytest.raises(APIError) as exc_info:
                c.enqueue("q", "k")
            assert exc_info.value.status_code == 500

    def test_default_priority(self):
        def handler(req: httpx.Request):
            body = json.loads(req.content)
            assert body["priority"] == 0
            return httpx.Response(200, json={"status": "enqueued"})

        with _make_client(handler) as c:
            c.enqueue("q", "k")

    def test_custom_client(self):
        def handler(req: httpx.Request):
            assert req.headers.get("x-custom") == "value"
            return httpx.Response(200, json={"status": "ok"})

        custom = httpx.Client(
            transport=_mock_transport(handler),
            headers={"x-custom": "value"},
        )
        with EnqueueClient("http://test", client=custom) as c:
            c.enqueue("q", "k")

    def test_retry_on_503(self):
        call_count = 0

        def handler(req: httpx.Request):
            nonlocal call_count
            call_count += 1
            if call_count < 2:
                return httpx.Response(503, text="unavailable")
            return httpx.Response(200, json={"status": "ok"})

        with _make_client(handler, retries=3) as c:
            c.enqueue("q", "k")
        assert call_count == 2


class TestAsyncEnqueueClient:
    @pytest.mark.asyncio
    async def test_enqueue(self):
        def handler(req: httpx.Request):
            body = json.loads(req.content)
            assert body["key"] == "pkg-1.0"
            return httpx.Response(200, json={"status": "enqueued"})

        async with AsyncEnqueueClient(
            "http://test",
            client=httpx.AsyncClient(transport=_mock_transport(handler)),
        ) as c:
            await c.enqueue("builds", "pkg-1.0", 5)

    @pytest.mark.asyncio
    async def test_enqueue_error(self):
        def handler(req: httpx.Request):
            return httpx.Response(500, text="fail")

        async with AsyncEnqueueClient(
            "http://test",
            client=httpx.AsyncClient(transport=_mock_transport(handler)),
        ) as c:
            with pytest.raises(APIError):
                await c.enqueue("q", "k")
