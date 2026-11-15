"""Caller-owned operation scope shared by Git and Forge transport calls."""

from collections.abc import Iterator
from contextlib import contextmanager
from contextvars import ContextVar
from threading import Event

from ._process import Budget

_current: ContextVar[Budget | None] = ContextVar("repokit_operation", default=None)


@contextmanager
def operation(*, timeout: float = 30, cancel: Event | None = None) -> Iterator[None]:
    """Bound a compound library operation; nested scopes never extend the outer deadline.

    Git cancellation kills and reaps its process. HTTP cancellation is checked between
    requests/reads; an in-flight socket remains bounded by its request timeout.
    This is an execution scope, not a transaction or a retry policy.
    """
    budget = Budget(timeout, cancel)
    outer = _current.get()
    if outer is not None:
        budget.deadline = min(budget.deadline, outer.deadline)
        budget.parent = outer
    token = _current.set(budget)
    try:
        budget.remaining()
        yield
    finally:
        _current.reset(token)


def current_budget(timeout: float) -> Budget:
    budget = Budget(timeout, None)
    outer = _current.get()
    if outer is not None:
        budget.deadline = min(budget.deadline, outer.deadline)
        budget.parent = outer
    return budget


def check_cancelled() -> None:
    """Check the caller scope without imposing a maintenance deadline."""
    outer = _current.get()
    if outer is not None:
        outer.remaining()
