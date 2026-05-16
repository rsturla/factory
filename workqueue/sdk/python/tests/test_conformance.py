"""Conformance tests: validate Python SDK against shared JSON fixtures."""

from __future__ import annotations

import json
from datetime import timedelta
from pathlib import Path

import pytest

from factory_workqueue._duration import parse_duration
from factory_workqueue.reconciler import (
    ProcessRequest,
    ProcessResponse,
    completed,
    converged,
    fan_out,
    reject,
    requeue_after,
)
from factory_workqueue.types import Status, WorkItem, valid_transition

FIXTURES = Path(__file__).resolve().parent.parent.parent.parent / "tests" / "sdk-conformance" / "fixtures"

_BUILDERS = {
    "completed": lambda args: completed(),
    "converged": lambda args: converged(),
    "requeue_after": lambda args: requeue_after(parse_duration(args[0])),
    "fan_out": lambda args: fan_out(*args),
    "reject": lambda args: reject(args[0]),
}


def _load(name: str) -> dict:
    return json.loads((FIXTURES / name).read_text())


class TestResponseBuilderConformance:
    @pytest.fixture(params=_load("response_builders.json")["tests"], ids=lambda t: t["name"])
    def case(self, request):
        return request.param

    def test_builder(self, case):
        builder = _BUILDERS[case["builder"]]
        result = builder(case["args"])
        result_dict = result.to_dict()

        expected = case["expected"]
        for k, v in expected.items():
            assert result_dict.get(k) == v, f"field {k}: {result_dict.get(k)} != {v}"


class TestProcessRequestConformance:
    @pytest.fixture(params=_load("process_request.json")["tests"], ids=lambda t: t["name"])
    def case(self, request):
        return request.param

    def test_deserialization(self, case):
        req = ProcessRequest.from_dict(case["json"])
        expected = case["expected"]
        assert req.key == expected["key"]
        assert req.attempt == expected["attempt"]
        assert req.priority == expected["priority"]
        assert req.trace_id == expected["trace_id"]


class TestStatusTransitionConformance:
    def test_valid_transitions(self):
        data = _load("status_transitions.json")
        for t in data["valid"]:
            assert valid_transition(Status(t["from"]), Status(t["to"])), \
                f"{t['from']} -> {t['to']} should be valid"

    def test_invalid_transitions(self):
        data = _load("status_transitions.json")
        for t in data["invalid"]:
            assert not valid_transition(Status(t["from"]), Status(t["to"])), \
                f"{t['from']} -> {t['to']} should be invalid"


class TestWorkItemConformance:
    @pytest.fixture(params=_load("work_item.json")["tests"], ids=lambda t: t["name"])
    def case(self, request):
        return request.param

    def test_deserialization(self, case):
        item = WorkItem.from_dict(case["json"])
        assert str(item.status) == case["expected_status"]
        assert item.key == case["expected_key"]
        assert item.priority == case["expected_priority"]
