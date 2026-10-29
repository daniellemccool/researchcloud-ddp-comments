"""File-lock helpers (fcntl.flock) for the desk."""
import fcntl
import os
import time
from contextlib import contextmanager


def render_lock(desk_root: str) -> str:
    return os.path.join(desk_root, ".render.lock")


def user_lock(desk_root: str, user: str) -> str:
    return os.path.join(desk_root, "locks", user)


@contextmanager
def flocked(path: str, timeout: float | None = None):
    """Hold an exclusive flock on `path` (created, with parents, if missing).

    Waits indefinitely when `timeout` is None; otherwise polls and raises
    TimeoutError once `timeout` seconds have passed. flock is per open file
    description, so a second flocked() on the same path blocks, even within
    the same process.
    """
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    fd = os.open(path, os.O_RDWR | os.O_CREAT, 0o600)
    try:
        if timeout is None:
            fcntl.flock(fd, fcntl.LOCK_EX)
        else:
            deadline = time.monotonic() + timeout
            while True:
                try:
                    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    break
                except BlockingIOError:
                    if time.monotonic() >= deadline:
                        raise TimeoutError(f"timed out waiting for lock {path}")
                    time.sleep(0.01)
        try:
            yield
        finally:
            fcntl.flock(fd, fcntl.LOCK_UN)
    finally:
        os.close(fd)
