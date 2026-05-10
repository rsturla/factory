"""Exception hierarchy for factory workqueue SDK."""

from __future__ import annotations


class WorkqueueError(Exception):
    pass


class APIError(WorkqueueError):
    def __init__(self, status_code: int, body: str) -> None:
        self.status_code = status_code
        self.body = body
        super().__init__(f"HTTP {status_code}: {body}")


class NotFoundError(APIError):
    def __init__(self, body: str = "not found") -> None:
        super().__init__(404, body)


class ConflictError(APIError):
    def __init__(self, body: str = "conflict") -> None:
        super().__init__(409, body)


class InvalidRequestError(APIError):
    def __init__(self, body: str = "bad request") -> None:
        super().__init__(400, body)
