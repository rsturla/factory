"""Cross-queue enqueue clients for reconciler fan-out."""

from __future__ import annotations

import time as _time
from typing import Any

import httpx

from .errors import APIError

_RETRY_STATUSES = {502, 503, 504}
_BACKOFF_SCHEDULE = [0.5, 1.0, 2.0]


class EnqueueClient:
    """Synchronous client for cross-queue enqueue via receiver /enqueue."""

    def __init__(
        self,
        endpoint: str,
        *,
        client: httpx.Client | None = None,
        timeout: float = 10.0,
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

    def __enter__(self) -> EnqueueClient:
        return self

    def __exit__(self, *args: Any) -> None:
        self.close()

    def enqueue(self, queue: str, key: str, priority: int = 0) -> None:
        last_exc: Exception | None = None
        for attempt in range(self._retries + 1):
            try:
                resp = self._client.post(
                    f"{self._endpoint}/enqueue",
                    json={"queue": queue, "key": key, "priority": priority},
                    headers={"Content-Type": "application/json"},
                )
                if resp.status_code in _RETRY_STATUSES and attempt < self._retries:
                    _time.sleep(_BACKOFF_SCHEDULE[min(attempt, len(_BACKOFF_SCHEDULE) - 1)])
                    continue
                if resp.status_code not in (200, 201):
                    raise APIError(resp.status_code, resp.text)
                return
            except httpx.ConnectError as e:
                last_exc = e
                if attempt < self._retries:
                    _time.sleep(_BACKOFF_SCHEDULE[min(attempt, len(_BACKOFF_SCHEDULE) - 1)])
                    continue
                raise
        raise last_exc  # type: ignore[misc]


class AsyncEnqueueClient:
    """Asynchronous client for cross-queue enqueue via receiver /enqueue."""

    def __init__(
        self,
        endpoint: str,
        *,
        client: httpx.AsyncClient | None = None,
        timeout: float = 10.0,
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

    async def __aenter__(self) -> AsyncEnqueueClient:
        return self

    async def __aexit__(self, *args: Any) -> None:
        await self.aclose()

    async def enqueue(self, queue: str, key: str, priority: int = 0) -> None:
        import asyncio

        last_exc: Exception | None = None
        for attempt in range(self._retries + 1):
            try:
                resp = await self._client.post(
                    f"{self._endpoint}/enqueue",
                    json={"queue": queue, "key": key, "priority": priority},
                    headers={"Content-Type": "application/json"},
                )
                if resp.status_code in _RETRY_STATUSES and attempt < self._retries:
                    await asyncio.sleep(_BACKOFF_SCHEDULE[min(attempt, len(_BACKOFF_SCHEDULE) - 1)])
                    continue
                if resp.status_code not in (200, 201):
                    raise APIError(resp.status_code, resp.text)
                return
            except httpx.ConnectError as e:
                last_exc = e
                if attempt < self._retries:
                    await asyncio.sleep(_BACKOFF_SCHEDULE[min(attempt, len(_BACKOFF_SCHEDULE) - 1)])
                    continue
                raise
        raise last_exc  # type: ignore[misc]
