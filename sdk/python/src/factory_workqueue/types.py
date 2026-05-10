"""Data types mirroring pkg/types/types.go."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone
from enum import StrEnum
from typing import Any


class Status(StrEnum):
    PENDING = "pending"
    CLAIMED = "claimed"
    RUNNING = "running"
    SUCCEEDED = "succeeded"
    FAILED = "failed"
    DEAD_LETTER = "dead_letter"


VALID_TRANSITIONS: dict[Status, set[Status]] = {
    Status.PENDING: {Status.CLAIMED, Status.FAILED},
    Status.CLAIMED: {Status.RUNNING, Status.FAILED, Status.PENDING},
    Status.RUNNING: {Status.SUCCEEDED, Status.FAILED, Status.PENDING},
    Status.FAILED: {Status.PENDING, Status.DEAD_LETTER},
    Status.DEAD_LETTER: {Status.PENDING},
}


def valid_transition(from_status: Status, to_status: Status) -> bool:
    allowed = VALID_TRANSITIONS.get(from_status)
    if allowed is None:
        return False
    return to_status in allowed


def _parse_dt(v: Any) -> datetime | None:
    if v is None:
        return None
    if isinstance(v, datetime):
        return v
    return datetime.fromisoformat(v)


def _format_dt(v: datetime | None) -> str | None:
    if v is None:
        return None
    if v.tzinfo is None:
        v = v.replace(tzinfo=timezone.utc)
    return v.isoformat()


@dataclass
class WorkItem:
    queue: str
    key: str
    status: Status
    priority: int = 0
    attempts: int = 0
    max_attempts: int = 0
    not_before: datetime | None = None
    lease_expires: datetime | None = None
    worker_id: str = ""
    error_message: str = ""
    created_at: datetime = field(default_factory=lambda: datetime.now(timezone.utc))
    updated_at: datetime = field(default_factory=lambda: datetime.now(timezone.utc))
    claimed_at: datetime | None = None
    completed_at: datetime | None = None

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {
            "queue": self.queue,
            "key": self.key,
            "status": str(self.status),
            "priority": self.priority,
            "attempts": self.attempts,
            "max_attempts": self.max_attempts,
            "created_at": _format_dt(self.created_at),
            "updated_at": _format_dt(self.updated_at),
        }
        if self.not_before is not None:
            d["not_before"] = _format_dt(self.not_before)
        if self.lease_expires is not None:
            d["lease_expires"] = _format_dt(self.lease_expires)
        if self.worker_id:
            d["worker_id"] = self.worker_id
        if self.error_message:
            d["error_message"] = self.error_message
        if self.claimed_at is not None:
            d["claimed_at"] = _format_dt(self.claimed_at)
        if self.completed_at is not None:
            d["completed_at"] = _format_dt(self.completed_at)
        return d

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> WorkItem:
        return cls(
            queue=d["queue"],
            key=d["key"],
            status=Status(d["status"]),
            priority=d.get("priority", 0),
            attempts=d.get("attempts", 0),
            max_attempts=d.get("max_attempts", 0),
            not_before=_parse_dt(d.get("not_before")),
            lease_expires=_parse_dt(d.get("lease_expires")),
            worker_id=d.get("worker_id", ""),
            error_message=d.get("error_message", ""),
            created_at=_parse_dt(d.get("created_at")) or datetime.now(timezone.utc),
            updated_at=_parse_dt(d.get("updated_at")) or datetime.now(timezone.utc),
            claimed_at=_parse_dt(d.get("claimed_at")),
            completed_at=_parse_dt(d.get("completed_at")),
        )


@dataclass
class QueueConfig:
    max_concurrency: int = 0
    max_retry: int = 0
    compute_backend: str = ""

    def to_dict(self) -> dict[str, Any]:
        return {
            "max_concurrency": self.max_concurrency,
            "max_retry": self.max_retry,
            "compute_backend": self.compute_backend,
        }

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> QueueConfig:
        return cls(
            max_concurrency=d.get("max_concurrency", 0),
            max_retry=d.get("max_retry", 0),
            compute_backend=d.get("compute_backend", ""),
        )


@dataclass
class QueueInfo:
    name: str
    max_concurrency: int = 0
    max_retry: int = 0
    compute_backend: str = ""
    paused: bool = False
    in_progress: int = 0
    counts: dict[str, int] = field(default_factory=dict)

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> QueueInfo:
        return cls(
            name=d["name"],
            max_concurrency=d.get("max_concurrency", 0),
            max_retry=d.get("max_retry", 0),
            compute_backend=d.get("compute_backend", ""),
            paused=d.get("paused", False),
            in_progress=d.get("in_progress", 0),
            counts=d.get("counts", {}),
        )


@dataclass
class ListFilter:
    queue: str
    status: Status | None = None
    limit: int = 0
    offset: int = 0

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {"queue": self.queue, "limit": self.limit, "offset": self.offset}
        if self.status is not None:
            d["status"] = str(self.status)
        return d


@dataclass
class HistoryEntry:
    id: int = 0
    queue: str = ""
    key: str = ""
    from_status: Status = Status.PENDING
    to_status: Status = Status.PENDING
    worker_id: str = ""
    error_message: str = ""
    attempt: int = 0
    trace_id: str = ""
    created_at: datetime = field(default_factory=lambda: datetime.now(timezone.utc))

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {
            "id": self.id,
            "queue": self.queue,
            "key": self.key,
            "from_status": str(self.from_status),
            "to_status": str(self.to_status),
            "created_at": _format_dt(self.created_at),
        }
        if self.worker_id:
            d["worker_id"] = self.worker_id
        if self.error_message:
            d["error_message"] = self.error_message
        if self.attempt:
            d["attempt"] = self.attempt
        if self.trace_id:
            d["trace_id"] = self.trace_id
        return d

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> HistoryEntry:
        return cls(
            id=d.get("id", 0),
            queue=d.get("queue", ""),
            key=d.get("key", ""),
            from_status=Status(d.get("from_status", "pending")),
            to_status=Status(d.get("to_status", "pending")),
            worker_id=d.get("worker_id", ""),
            error_message=d.get("error_message", ""),
            attempt=d.get("attempt", 0),
            trace_id=d.get("trace_id", ""),
            created_at=_parse_dt(d.get("created_at")) or datetime.now(timezone.utc),
        )


@dataclass
class WorkerLease:
    worker_id: str = ""
    queue: str = ""
    compute_backend: str = ""
    hostname: str = ""
    started_at: datetime = field(default_factory=lambda: datetime.now(timezone.utc))
    last_heartbeat: datetime = field(default_factory=lambda: datetime.now(timezone.utc))
    items_processed: int = 0
    status: str = ""

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> WorkerLease:
        return cls(
            worker_id=d.get("worker_id", ""),
            queue=d.get("queue", ""),
            compute_backend=d.get("compute_backend", ""),
            hostname=d.get("hostname", ""),
            started_at=_parse_dt(d.get("started_at")) or datetime.now(timezone.utc),
            last_heartbeat=_parse_dt(d.get("last_heartbeat")) or datetime.now(timezone.utc),
            items_processed=d.get("items_processed", 0),
            status=d.get("status", ""),
        )


@dataclass
class BatchEnqueueItem:
    key: str
    priority: int = 0
    not_before: datetime | None = None

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {"key": self.key, "priority": self.priority}
        if self.not_before is not None:
            d["not_before"] = _format_dt(self.not_before)
        return d
