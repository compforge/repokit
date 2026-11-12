"""Static Make contracts using the same upstream grammar as the Go SDK."""

from __future__ import annotations

import time
from dataclasses import dataclass

import tree_sitter_make
from tree_sitter import Language, Parser, Point

from .._process import Budget

_LANGUAGE = Language(tree_sitter_make.language())


@dataclass(frozen=True)
class MakeContract:
    targets: frozenset[str] = frozenset()
    variables: frozenset[str] = frozenset()
    problem: str = ""

    def target(self, check: str) -> str:
        candidates = ("lint-ci", "lint") if check == "lint" else ("test", "test-ci", "test-local")
        return next((name for name in candidates if name in self.targets), "")


def read_contract(source: bytes, budget: Budget) -> MakeContract:
    """Read literal top-level rules without executing Make or following includes.

    +spec=Go/Python share Make grammar semantics and a 2 MiB/2 second syntax budget.
    +link=component://docs/validation.md
    """
    if not source:
        return MakeContract()
    if len(source) > 2 << 20:
        return MakeContract(problem="Makefile exceeds 2 MiB syntax budget")
    deadline = time.monotonic() + min(2, budget.remaining())

    def read(offset: int, _point: Point) -> bytes:
        return source[offset : offset + 16384]

    def stop(_offset: int, _has_error: bool) -> bool:
        try:
            budget.remaining()
        except (TimeoutError, InterruptedError):
            return True
        return time.monotonic() >= deadline

    try:
        tree = Parser(_LANGUAGE).parse(read, progress_callback=stop)
    except ValueError:
        return MakeContract(problem="Makefile syntax unavailable")
    if tree is None:
        return MakeContract(problem="Makefile syntax unavailable")
    if tree.root_node.has_error:
        return MakeContract(problem="Makefile contains unsupported or invalid syntax")
    targets: set[str] = set()
    for rule in tree.root_node.named_children:
        if rule.type in {"word", "list"}:
            return MakeContract(problem="Makefile contains unsupported or invalid syntax")
        if rule.type != "rule":
            continue
        for node in rule.named_children:
            if node.type == "targets":
                text = source[node.start_byte : node.end_byte].decode().replace("\\\n", " ")
                targets.update(name for name in text.split() if not any(c in name for c in "$%\\"))
    variables: set[str] = set()
    pending = [tree.root_node]
    while pending:
        node = pending.pop()
        if node.type == "variable_reference":
            variables.update(
                source[word.start_byte : word.end_byte].decode()
                for word in node.named_children
                if word.type == "word"
            )
        pending.extend(node.named_children)
    return MakeContract(frozenset(targets), frozenset(variables))
