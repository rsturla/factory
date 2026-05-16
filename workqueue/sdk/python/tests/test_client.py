import json
from datetime import timedelta

import httpx
import pytest

from factory_workqueue.client import AsyncWorkqueueClient, WorkqueueClient
from factory_workqueue.errors import ConflictError, InvalidRequestError, NotFoundError
from factory_workqueue.types import ListFilter, QueueConfig, Status


def _mock_transport(handler):
    """Create httpx mock transport from a handler function."""
    return httpx.MockTransport(handler)


def _ok_json(data):
    return httpx.Response(200, json=data)


def _make_client(handler, retries=0) -> WorkqueueClient:
    return WorkqueueClient(
        "http://test",
        client=httpx.Client(transport=_mock_transport(handler)),
        retries=retries,
    )


def _make_async_client(handler, retries=0) -> AsyncWorkqueueClient:
    return AsyncWorkqueueClient(
        "http://test",
        client=httpx.AsyncClient(transport=_mock_transport(handler)),
        retries=retries,
    )


class TestWorkqueueClient:
    def test_enqueue(self):
        def handler(req: httpx.Request):
            assert req.url.path == "/wq/enqueue"
            body = json.loads(req.content)
            assert body["queue"] == "builds"
            assert body["key"] == "pkg-1.0"
            assert body["priority"] == 5
            return _ok_json({"status": "ok"})

        with _make_client(handler) as c:
            c.enqueue("builds", "pkg-1.0", 5)

    def test_claim_batch(self):
        def handler(req: httpx.Request):
            body = json.loads(req.content)
            assert body["batch_size"] == 3
            assert body["worker_id"] == "w-1"
            return _ok_json([{
                "queue": "q", "key": "k1", "status": "claimed",
                "created_at": "2026-05-10T12:00:00+00:00",
                "updated_at": "2026-05-10T12:00:00+00:00",
            }])

        with _make_client(handler) as c:
            items = c.claim_batch("q", 3, "w-1", timedelta(hours=1))
            assert len(items) == 1
            assert items[0].key == "k1"
            assert items[0].status == Status.CLAIMED

    def test_complete(self):
        def handler(req: httpx.Request):
            body = json.loads(req.content)
            assert body["queue"] == "q"
            assert body["key"] == "k"
            return _ok_json({"status": "ok"})

        with _make_client(handler) as c:
            c.complete("q", "k")

    def test_fail(self):
        def handler(req: httpx.Request):
            body = json.loads(req.content)
            assert body["error"] == "oops"
            return _ok_json({"status": "ok"})

        with _make_client(handler) as c:
            c.fail("q", "k", "oops")

    def test_count_by_status(self):
        def handler(req: httpx.Request):
            return _ok_json({"pending": 5, "running": 3})

        with _make_client(handler) as c:
            counts = c.count_by_status("q")
            assert counts[Status.PENDING] == 5
            assert counts[Status.RUNNING] == 3

    def test_get_item(self):
        def handler(req: httpx.Request):
            return _ok_json({
                "queue": "q", "key": "k", "status": "pending",
                "priority": 10,
                "created_at": "2026-05-10T12:00:00+00:00",
                "updated_at": "2026-05-10T12:00:00+00:00",
            })

        with _make_client(handler) as c:
            item = c.get_item("q", "k")
            assert item.key == "k"
            assert item.priority == 10

    def test_list_queues(self):
        def handler(req: httpx.Request):
            return _ok_json([{"name": "builds", "max_concurrency": 10}])

        with _make_client(handler) as c:
            queues = c.list_queues()
            assert len(queues) == 1
            assert queues[0].name == "builds"

    def test_is_queue_paused(self):
        def handler(req: httpx.Request):
            return _ok_json({"paused": True})

        with _make_client(handler) as c:
            assert c.is_queue_paused("q") is True

    def test_ping(self):
        def handler(req: httpx.Request):
            assert req.url.path == "/wq/ping"
            return _ok_json({"status": "ok"})

        with _make_client(handler) as c:
            c.ping()

    def test_ensure_queue(self):
        def handler(req: httpx.Request):
            body = json.loads(req.content)
            assert body["config"]["max_concurrency"] == 10
            return _ok_json({"status": "ok"})

        with _make_client(handler) as c:
            c.ensure_queue("q", QueueConfig(max_concurrency=10, max_retry=5))

    def test_transition(self):
        def handler(req: httpx.Request):
            body = json.loads(req.content)
            assert body["from"] == "pending"
            assert body["to"] == "claimed"
            return _ok_json({"status": "ok"})

        with _make_client(handler) as c:
            c.transition("q", "k", Status.PENDING, Status.CLAIMED)

    def test_list_items(self):
        def handler(req: httpx.Request):
            body = json.loads(req.content)
            assert body["queue"] == "q"
            assert body["status"] == "pending"
            return _ok_json([{
                "queue": "q", "key": "k1", "status": "pending",
                "created_at": "2026-05-10T12:00:00+00:00",
                "updated_at": "2026-05-10T12:00:00+00:00",
            }])

        with _make_client(handler) as c:
            items = c.list(ListFilter(queue="q", status=Status.PENDING, limit=10))
            assert len(items) == 1


class TestErrorMapping:
    def test_404(self):
        def handler(req: httpx.Request):
            return httpx.Response(404, text="work item not found")

        with _make_client(handler) as c:
            with pytest.raises(NotFoundError):
                c.get_item("q", "k")

    def test_409(self):
        def handler(req: httpx.Request):
            return httpx.Response(409, text="status conflict")

        with _make_client(handler) as c:
            with pytest.raises(ConflictError):
                c.complete("q", "k")

    def test_400(self):
        def handler(req: httpx.Request):
            return httpx.Response(400, text="missing key")

        with _make_client(handler) as c:
            with pytest.raises(InvalidRequestError):
                c.enqueue("q", "", 0)


class TestAsyncWorkqueueClient:
    @pytest.mark.asyncio
    async def test_enqueue(self):
        def handler(req: httpx.Request):
            body = json.loads(req.content)
            assert body["key"] == "pkg-1.0"
            return _ok_json({"status": "ok"})

        async with _make_async_client(handler) as c:
            await c.enqueue("builds", "pkg-1.0", 5)

    @pytest.mark.asyncio
    async def test_claim_batch(self):
        def handler(req: httpx.Request):
            return _ok_json([{
                "queue": "q", "key": "k1", "status": "claimed",
                "created_at": "2026-05-10T12:00:00+00:00",
                "updated_at": "2026-05-10T12:00:00+00:00",
            }])

        async with _make_async_client(handler) as c:
            items = await c.claim_batch("q", 1, "w-1", timedelta(hours=1))
            assert len(items) == 1

    @pytest.mark.asyncio
    async def test_error_mapping(self):
        def handler(req: httpx.Request):
            return httpx.Response(404, text="not found")

        async with _make_async_client(handler) as c:
            with pytest.raises(NotFoundError):
                await c.get_item("q", "k")

    @pytest.mark.asyncio
    async def test_ping(self):
        def handler(req: httpx.Request):
            return _ok_json({"status": "ok"})

        async with _make_async_client(handler) as c:
            await c.ping()


class TestRetry:
    def test_retries_on_503(self):
        call_count = 0

        def handler(req: httpx.Request):
            nonlocal call_count
            call_count += 1
            if call_count < 3:
                return httpx.Response(503, text="unavailable")
            return _ok_json({"status": "ok"})

        with _make_client(handler, retries=3) as c:
            c.ping()
        assert call_count == 3

    def test_no_retry_on_400(self):
        call_count = 0

        def handler(req: httpx.Request):
            nonlocal call_count
            call_count += 1
            return httpx.Response(400, text="bad request")

        with _make_client(handler, retries=3) as c:
            with pytest.raises(InvalidRequestError):
                c.enqueue("q", "k", 0)
        assert call_count == 1

    def test_no_retry_on_404(self):
        call_count = 0

        def handler(req: httpx.Request):
            nonlocal call_count
            call_count += 1
            return httpx.Response(404, text="not found")

        with _make_client(handler, retries=3) as c:
            with pytest.raises(NotFoundError):
                c.get_item("q", "k")
        assert call_count == 1

    def test_retries_disabled(self):
        call_count = 0

        def handler(req: httpx.Request):
            nonlocal call_count
            call_count += 1
            return httpx.Response(503, text="unavailable")

        from factory_workqueue.errors import APIError
        with _make_client(handler, retries=0) as c:
            with pytest.raises(APIError):
                c.ping()
        assert call_count == 1


class TestCustomClient:
    def test_accepts_custom_httpx_client(self):
        def handler(req: httpx.Request):
            assert req.headers.get("x-custom") == "val"
            return _ok_json({"status": "ok"})

        custom = httpx.Client(
            transport=_mock_transport(handler),
            headers={"x-custom": "val"},
        )
        with WorkqueueClient("http://test", client=custom) as c:
            c.ping()

    def test_custom_client_not_closed(self):
        def handler(req: httpx.Request):
            return _ok_json({"status": "ok"})

        custom = httpx.Client(transport=_mock_transport(handler))
        c = WorkqueueClient("http://test", client=custom)
        c.close()
        # Custom client should still be open
        assert not custom.is_closed
