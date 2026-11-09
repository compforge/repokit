"""Bounded subprocesses sharing an operation deadline."""

import math
import os
import selectors
import signal
import subprocess
import time
from pathlib import Path
from threading import Event

from ._git_environment import checkout_environment


class Budget:
    def __init__(self, timeout: float, cancel: Event | None):
        if not math.isfinite(timeout) or timeout <= 0:
            raise ValueError("timeout must be positive and finite")
        self.parent: Budget | None = None
        self.deadline = time.monotonic() + timeout
        self.cancel = cancel

    def remaining(self) -> float:
        if self.parent is not None:
            self.parent.remaining()
        if self.cancel is not None and self.cancel.is_set():
            raise InterruptedError("operation cancelled")
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("operation timed out")
        return remaining


def run(
    root: Path,
    args: list[str],
    budget: Budget,
    *,
    input: bytes = b"",
    allow_missing: bool = False,
    max_output: int = 16 << 20,
) -> bytes:
    return run_command(
        ["git", "-C", str(root), *args],
        budget,
        input=input,
        env=checkout_environment(),
        allow_missing=allow_missing,
        max_output=max_output,
    ).stdout


def run_command(
    command: list[str],
    budget: Budget,
    *,
    input: bytes = b"",
    allow_missing: bool = False,
    max_output: int = 16 << 20,
    env: dict[str, str] | None = None,
    cwd: Path | None = None,
    process_group: bool = False,
) -> subprocess.CompletedProcess[bytes]:
    budget.remaining()
    process = subprocess.Popen(
        command,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=env,
        cwd=cwd,
        start_new_session=process_group,
    )
    assert process.stdin is not None and process.stdout is not None and process.stderr is not None
    stdout, stderr = bytearray(), bytearray()
    pending = memoryview(input)
    try:
        # Drain both outputs while feeding a batch. communicate() buffers without a
        # capacity bound; waiting before draining can deadlock on a full pipe.
        with selectors.DefaultSelector() as selector:
            for pipe, target in ((process.stdout, stdout), (process.stderr, stderr)):
                os.set_blocking(pipe.fileno(), False)
                selector.register(pipe, selectors.EVENT_READ, target)
            if pending:
                os.set_blocking(process.stdin.fileno(), False)
                selector.register(process.stdin, selectors.EVENT_WRITE)
            else:
                process.stdin.close()
            while selector.get_map():
                remaining = budget.remaining()
                for key, event in selector.select(min(remaining, 0.05)):
                    if event == selectors.EVENT_WRITE:
                        try:
                            used = os.write(key.fd, pending[:65536])
                            pending = pending[used:]
                        except BrokenPipeError:
                            pending = memoryview(b"")
                        if not pending:
                            selector.unregister(key.fileobj)
                            process.stdin.close()
                    else:
                        chunk = os.read(key.fd, 65536)
                        if not chunk:
                            selector.unregister(key.fileobj)
                            continue
                        target = key.data
                        limit = max_output if target is stdout else 65536
                        if len(target) + len(chunk) > limit:
                            raise ValueError(f"Process output exceeds {limit} bytes")
                        target.extend(chunk)
            while process.poll() is None:
                remaining = budget.remaining()
                try:
                    process.wait(timeout=min(remaining, 0.05))
                except subprocess.TimeoutExpired:
                    continue
        budget.remaining()
        if process.returncode != 0 and not (allow_missing and process.returncode == 1):
            raise subprocess.CalledProcessError(
                process.returncode, command, bytes(stdout), bytes(stderr)
            )
        return subprocess.CompletedProcess(
            command, process.returncode, bytes(stdout), bytes(stderr)
        )
    finally:
        # Timeout, cancellation and output overflow must not leave Git running.
        if process_group:
            # Package-manager lifecycle scripts may outlive their parent on cancellation.
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        elif process.poll() is None:
            process.kill()
        process.wait()
        process.stdin.close()
        process.stdout.close()
        process.stderr.close()
