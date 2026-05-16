from datetime import datetime, timezone

from factory_workqueue.types import (
    BatchEnqueueItem,
    HistoryEntry,
    ListFilter,
    QueueConfig,
    QueueInfo,
    Status,
    WorkItem,
    valid_transition,
)


class TestStatus:
    def test_values(self):
        assert Status.PENDING == "pending"
        assert Status.CLAIMED == "claimed"
        assert Status.RUNNING == "running"
        assert Status.SUCCEEDED == "succeeded"
        assert Status.FAILED == "failed"
        assert Status.DEAD_LETTER == "dead_letter"

    def test_from_string(self):
        assert Status("pending") == Status.PENDING


class TestValidTransition:
    def test_pending_to_claimed(self):
        assert valid_transition(Status.PENDING, Status.CLAIMED)

    def test_pending_to_running_invalid(self):
        assert not valid_transition(Status.PENDING, Status.RUNNING)

    def test_running_to_succeeded(self):
        assert valid_transition(Status.RUNNING, Status.SUCCEEDED)

    def test_failed_to_dead_letter(self):
        assert valid_transition(Status.FAILED, Status.DEAD_LETTER)

    def test_dead_letter_to_pending(self):
        assert valid_transition(Status.DEAD_LETTER, Status.PENDING)

    def test_succeeded_to_anything_invalid(self):
        for s in Status:
            assert not valid_transition(Status.SUCCEEDED, s)


class TestWorkItem:
    def test_roundtrip(self):
        now = datetime(2026, 5, 10, 12, 0, 0, tzinfo=timezone.utc)
        item = WorkItem(
            queue="test",
            key="key-1",
            status=Status.PENDING,
            priority=5,
            created_at=now,
            updated_at=now,
        )
        d = item.to_dict()
        assert d["queue"] == "test"
        assert d["key"] == "key-1"
        assert d["status"] == "pending"
        assert d["priority"] == 5
        assert "worker_id" not in d  # omitempty

        restored = WorkItem.from_dict(d)
        assert restored.queue == item.queue
        assert restored.key == item.key
        assert restored.status == item.status
        assert restored.priority == item.priority

    def test_from_dict_with_optional_fields(self):
        d = {
            "queue": "q",
            "key": "k",
            "status": "running",
            "worker_id": "w-1",
            "error_message": "oops",
            "claimed_at": "2026-05-10T12:00:00+00:00",
        }
        item = WorkItem.from_dict(d)
        assert item.worker_id == "w-1"
        assert item.error_message == "oops"
        assert item.claimed_at is not None


class TestQueueConfig:
    def test_roundtrip(self):
        cfg = QueueConfig(max_concurrency=10, max_retry=5, compute_backend="kubernetes")
        d = cfg.to_dict()
        restored = QueueConfig.from_dict(d)
        assert restored.max_concurrency == 10
        assert restored.max_retry == 5
        assert restored.compute_backend == "kubernetes"


class TestQueueInfo:
    def test_from_dict(self):
        d = {
            "name": "builds",
            "max_concurrency": 20,
            "paused": True,
            "counts": {"pending": 5, "running": 3},
        }
        info = QueueInfo.from_dict(d)
        assert info.name == "builds"
        assert info.paused is True
        assert info.counts["pending"] == 5


class TestListFilter:
    def test_to_dict_without_status(self):
        f = ListFilter(queue="test", limit=10)
        d = f.to_dict()
        assert "status" not in d

    def test_to_dict_with_status(self):
        f = ListFilter(queue="test", status=Status.PENDING, limit=10)
        d = f.to_dict()
        assert d["status"] == "pending"


class TestHistoryEntry:
    def test_roundtrip(self):
        now = datetime(2026, 5, 10, 12, 0, 0, tzinfo=timezone.utc)
        entry = HistoryEntry(
            id=1,
            queue="test",
            key="k",
            from_status=Status.PENDING,
            to_status=Status.CLAIMED,
            worker_id="w-1",
            created_at=now,
        )
        d = entry.to_dict()
        restored = HistoryEntry.from_dict(d)
        assert restored.from_status == Status.PENDING
        assert restored.to_status == Status.CLAIMED
        assert restored.worker_id == "w-1"


class TestBatchEnqueueItem:
    def test_to_dict_minimal(self):
        item = BatchEnqueueItem(key="pkg-1.0")
        d = item.to_dict()
        assert d == {"key": "pkg-1.0", "priority": 0}
        assert "not_before" not in d

    def test_to_dict_with_not_before(self):
        nb = datetime(2026, 6, 1, tzinfo=timezone.utc)
        item = BatchEnqueueItem(key="pkg-1.0", priority=10, not_before=nb)
        d = item.to_dict()
        assert "not_before" in d
