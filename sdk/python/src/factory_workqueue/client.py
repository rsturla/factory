"""Workqueue HTTP clients for standalone workers."""

from __future__ import annotations

import time as _time
from datetime import timedelta
from typing import Any

import httpx

from ._duration import format_duration
from .errors import APIError, ConflictError, InvalidRequestError, NotFoundError
from .types import (
    BatchEnqueueItem,
    HistoryEntry,
    ListFilter,
    QueueConfig,
    QueueInfo,
    Status,
    WorkItem,
    WorkerLease,
)

_RETRY_STATUSES = {502, 503, 504}
_BACKOFF_SCHEDULE = [0.5, 1.0, 2.0]


def _raise_for_status(resp: httpx.Response) -> None:
    if 200 <= resp.status_code < 300:
        return
    body = resp.text
    if resp.status_code == 404:
        raise NotFoundError(body)
    if resp.status_code == 409:
        raise ConflictError(body)
    if resp.status_code == 400:
        raise InvalidRequestError(body)
    raise APIError(resp.status_code, body)


class WorkqueueClient:
    """Synchronous HTTP client for /wq/* endpoints."""

    def __init__(
        self,
        endpoint: str,
        *,
        client: httpx.Client | None = None,
        timeout: float = 30.0,
        headers: dict[str, str] | None = None,
        retries: int = 3,
    ) -> None:
        self._endpoint = endpoint.rstrip("/")
        self._retries = retries
        if client is not None:
            self._client = client
            self._owns_client = False
        else:
            self._client = httpx.Client(timeout=timeout, headers=headers or {})
            self._owns_client = True

    def close(self) -> None:
        if self._owns_client:
            self._client.close()

    def __enter__(self) -> WorkqueueClient:
        return self

    def __exit__(self, *args: Any) -> None:
        self.close()

    def _post(self, path: str, payload: Any = None) -> httpx.Response:
        last_exc: Exception | None = None
        for attempt in range(self._retries + 1):
            try:
                resp = self._client.post(
                    f"{self._endpoint}{path}",
                    json=payload,
                    headers={"Content-Type": "application/json"},
                )
                if resp.status_code in _RETRY_STATUSES and attempt < self._retries:
                    _time.sleep(_BACKOFF_SCHEDULE[min(attempt, len(_BACKOFF_SCHEDULE) - 1)])
                    continue
                _raise_for_status(resp)
                return resp
            except httpx.ConnectError as e:
                last_exc = e
                if attempt < self._retries:
                    _time.sleep(_BACKOFF_SCHEDULE[min(attempt, len(_BACKOFF_SCHEDULE) - 1)])
                    continue
                raise
        raise last_exc  # type: ignore[misc]

    def enqueue(self, queue: str, key: str, priority: int = 0) -> None:
        self._post("/wq/enqueue", {"queue": queue, "key": key, "priority": priority})

    def enqueue_batch(self, queue: str, items: list[BatchEnqueueItem]) -> int:
        resp = self._post("/wq/enqueue-batch", {
            "queue": queue,
            "items": [i.to_dict() for i in items],
        })
        return resp.json().get("count", 0)

    def claim_batch(
        self,
        queue: str,
        batch_size: int,
        worker_id: str,
        lease_duration: timedelta,
    ) -> list[WorkItem]:
        resp = self._post("/wq/claim", {
            "queue": queue,
            "batch_size": batch_size,
            "worker_id": worker_id,
            "lease_duration": format_duration(lease_duration),
        })
        return [WorkItem.from_dict(d) for d in resp.json()]

    def complete(self, queue: str, key: str) -> None:
        self._post("/wq/complete", {"queue": queue, "key": key})

    def fail(self, queue: str, key: str, error_msg: str) -> None:
        self._post("/wq/fail", {"queue": queue, "key": key, "error": error_msg})

    def requeue(self, queue: str, key: str) -> None:
        self._post("/wq/requeue", {"queue": queue, "key": key})

    def deadletter(self, queue: str, key: str) -> None:
        self._post("/wq/deadletter", {"queue": queue, "key": key})

    def extend_lease(self, queue: str, key: str, duration: timedelta) -> None:
        self._post("/wq/heartbeat", {
            "queue": queue, "key": key, "duration": format_duration(duration),
        })

    def transition(
        self,
        queue: str,
        key: str,
        from_status: Status,
        to_status: Status,
    ) -> None:
        self._post("/wq/transition", {
            "queue": queue, "key": key, "from": str(from_status), "to": str(to_status),
        })

    def ensure_queue(self, queue: str, config: QueueConfig) -> None:
        self._post("/wq/ensure-queue", {"queue": queue, "config": config.to_dict()})

    def repair_counter(self, queue: str) -> None:
        self._post("/wq/repair", {"queue": queue})

    def set_queue_paused(self, queue: str, paused: bool) -> None:
        self._post("/wq/set-paused", {"queue": queue, "paused": paused})

    def is_queue_paused(self, queue: str) -> bool:
        resp = self._post("/wq/is-paused", {"queue": queue})
        return resp.json().get("paused", False)

    def count_by_status(self, queue: str) -> dict[Status, int]:
        resp = self._post("/wq/count", {"queue": queue})
        return {Status(k): v for k, v in resp.json().items()}

    def list(self, filter: ListFilter) -> list[WorkItem]:
        resp = self._post("/wq/list", filter.to_dict())
        return [WorkItem.from_dict(d) for d in resp.json()]

    def get_item(self, queue: str, key: str) -> WorkItem:
        resp = self._post("/wq/get-item", {"queue": queue, "key": key})
        return WorkItem.from_dict(resp.json())

    def list_queues(self) -> list[QueueInfo]:
        resp = self._post("/wq/list-queues")
        return [QueueInfo.from_dict(d) for d in resp.json()]

    def list_workers(self, queue: str) -> list[WorkerLease]:
        resp = self._post("/wq/list-workers", {"queue": queue})
        return [WorkerLease.from_dict(d) for d in resp.json()]

    def purge_dead_letters(self, queue: str) -> int:
        resp = self._post("/wq/purge-dead-letters", {"queue": queue})
        return resp.json().get("count", 0)

    def list_expired_leases(self, queue: str, limit: int) -> list[WorkItem]:
        resp = self._post("/wq/list-expired-leases", {"queue": queue, "limit": limit})
        return [WorkItem.from_dict(d) for d in resp.json()]

    def record_history(self, entry: HistoryEntry) -> None:
        self._post("/wq/record-history", entry.to_dict())

    def get_item_history(self, queue: str, key: str) -> list[HistoryEntry]:
        resp = self._post("/wq/get-history", {"queue": queue, "key": key})
        return [HistoryEntry.from_dict(d) for d in resp.json()]

    def ping(self) -> None:
        self._post("/wq/ping")


class AsyncWorkqueueClient:
    """Asynchronous HTTP client for /wq/* endpoints."""

    def __init__(
        self,
        endpoint: str,
        *,
        client: httpx.AsyncClient | None = None,
        timeout: float = 30.0,
        headers: dict[str, str] | None = None,
        retries: int = 3,
    ) -> None:
        self._endpoint = endpoint.rstrip("/")
        self._retries = retries
        if client is not None:
            self._client = client
            self._owns_client = False
        else:
            self._client = httpx.AsyncClient(timeout=timeout, headers=headers or {})
            self._owns_client = True

    async def aclose(self) -> None:
        if self._owns_client:
            await self._client.aclose()

    async def __aenter__(self) -> AsyncWorkqueueClient:
        return self

    async def __aexit__(self, *args: Any) -> None:
        await self.aclose()

    async def _post(self, path: str, payload: Any = None) -> httpx.Response:
        import asyncio

        last_exc: Exception | None = None
        for attempt in range(self._retries + 1):
            try:
                resp = await self._client.post(
                    f"{self._endpoint}{path}",
                    json=payload,
                    headers={"Content-Type": "application/json"},
                )
                if resp.status_code in _RETRY_STATUSES and attempt < self._retries:
                    await asyncio.sleep(_BACKOFF_SCHEDULE[min(attempt, len(_BACKOFF_SCHEDULE) - 1)])
                    continue
                _raise_for_status(resp)
                return resp
            except httpx.ConnectError as e:
                last_exc = e
                if attempt < self._retries:
                    await asyncio.sleep(_BACKOFF_SCHEDULE[min(attempt, len(_BACKOFF_SCHEDULE) - 1)])
                    continue
                raise
        raise last_exc  # type: ignore[misc]

    async def enqueue(self, queue: str, key: str, priority: int = 0) -> None:
        await self._post("/wq/enqueue", {"queue": queue, "key": key, "priority": priority})

    async def enqueue_batch(self, queue: str, items: list[BatchEnqueueItem]) -> int:
        resp = await self._post("/wq/enqueue-batch", {
            "queue": queue,
            "items": [i.to_dict() for i in items],
        })
        return resp.json().get("count", 0)

    async def claim_batch(
        self,
        queue: str,
        batch_size: int,
        worker_id: str,
        lease_duration: timedelta,
    ) -> list[WorkItem]:
        resp = await self._post("/wq/claim", {
            "queue": queue,
            "batch_size": batch_size,
            "worker_id": worker_id,
            "lease_duration": format_duration(lease_duration),
        })
        return [WorkItem.from_dict(d) for d in resp.json()]

    async def complete(self, queue: str, key: str) -> None:
        await self._post("/wq/complete", {"queue": queue, "key": key})

    async def fail(self, queue: str, key: str, error_msg: str) -> None:
        await self._post("/wq/fail", {"queue": queue, "key": key, "error": error_msg})

    async def requeue(self, queue: str, key: str) -> None:
        await self._post("/wq/requeue", {"queue": queue, "key": key})

    async def deadletter(self, queue: str, key: str) -> None:
        await self._post("/wq/deadletter", {"queue": queue, "key": key})

    async def extend_lease(self, queue: str, key: str, duration: timedelta) -> None:
        await self._post("/wq/heartbeat", {
            "queue": queue, "key": key, "duration": format_duration(duration),
        })

    async def transition(
        self,
        queue: str,
        key: str,
        from_status: Status,
        to_status: Status,
    ) -> None:
        await self._post("/wq/transition", {
            "queue": queue, "key": key, "from": str(from_status), "to": str(to_status),
        })

    async def ensure_queue(self, queue: str, config: QueueConfig) -> None:
        await self._post("/wq/ensure-queue", {"queue": queue, "config": config.to_dict()})

    async def repair_counter(self, queue: str) -> None:
        await self._post("/wq/repair", {"queue": queue})

    async def set_queue_paused(self, queue: str, paused: bool) -> None:
        await self._post("/wq/set-paused", {"queue": queue, "paused": paused})

    async def is_queue_paused(self, queue: str) -> bool:
        resp = await self._post("/wq/is-paused", {"queue": queue})
        return resp.json().get("paused", False)

    async def count_by_status(self, queue: str) -> dict[Status, int]:
        resp = await self._post("/wq/count", {"queue": queue})
        return {Status(k): v for k, v in resp.json().items()}

    async def list(self, filter: ListFilter) -> list[WorkItem]:
        resp = await self._post("/wq/list", filter.to_dict())
        return [WorkItem.from_dict(d) for d in resp.json()]

    async def get_item(self, queue: str, key: str) -> WorkItem:
        resp = await self._post("/wq/get-item", {"queue": queue, "key": key})
        return WorkItem.from_dict(resp.json())

    async def list_queues(self) -> list[QueueInfo]:
        resp = await self._post("/wq/list-queues")
        return [QueueInfo.from_dict(d) for d in resp.json()]

    async def list_workers(self, queue: str) -> list[WorkerLease]:
        resp = await self._post("/wq/list-workers", {"queue": queue})
        return [WorkerLease.from_dict(d) for d in resp.json()]

    async def purge_dead_letters(self, queue: str) -> int:
        resp = await self._post("/wq/purge-dead-letters", {"queue": queue})
        return resp.json().get("count", 0)

    async def list_expired_leases(self, queue: str, limit: int) -> list[WorkItem]:
        resp = await self._post("/wq/list-expired-leases", {"queue": queue, "limit": limit})
        return [WorkItem.from_dict(d) for d in resp.json()]

    async def record_history(self, entry: HistoryEntry) -> None:
        await self._post("/wq/record-history", entry.to_dict())

    async def get_item_history(self, queue: str, key: str) -> list[HistoryEntry]:
        resp = await self._post("/wq/get-history", {"queue": queue, "key": key})
        return [HistoryEntry.from_dict(d) for d in resp.json()]

    async def ping(self) -> None:
        await self._post("/wq/ping")
