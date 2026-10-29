"""Session registry: sessions.tsv maps each user to an index (hence a noVNC port).

Format: `user<TAB>index<TAB>status`, status in {live, retired}. Legacy
two-column rows (`user<TAB>index`) are live. Each user appears in one row.

Locking contract: every writer (`allocate`, `retire`) does an unlocked
read-modify-write, so the CALLER must hold flocked(render_lock(desk_root))
around it. The readers (`rows`, `live`, `port`) are lock-free; writes replace
the file atomically, so a read sees either the old or the new content.
"""
import os
import re
import tempfile
from dataclasses import dataclass

USER_RE = re.compile(r"^[a-z][a-z0-9._-]{0,31}\Z")


class RegistryFull(RuntimeError):
    pass


@dataclass
class Row:
    user: str
    index: int
    status: str  # "live" | "retired"


class Registry:
    def __init__(self, desk_root: str, max_sessions: int = 99, port_base: int = 5800):
        self.desk_root = desk_root
        self.max_sessions = max_sessions
        self.port_base = port_base
        self.path = os.path.join(desk_root, "sessions.tsv")

    def rows(self) -> list[Row]:
        try:
            with open(self.path) as f:
                text = f.read()
        except FileNotFoundError:
            return []
        out = []
        for line in text.splitlines():
            parts = line.split("\t")
            if len(parts) < 2 or not parts[0].strip():
                continue
            status = parts[2].strip() if len(parts) > 2 and parts[2].strip() else "live"
            out.append(Row(parts[0].strip(), int(parts[1]), status))
        return out

    def live(self) -> dict[str, Row]:
        return {r.user: r for r in self.rows() if r.status == "live"}

    def port(self, user: str) -> int | None:
        row = self.live().get(user)
        return self.port_base + row.index if row else None

    def allocate(self, user: str) -> Row:
        """Return the live row for `user`, creating or reactivating it.

        Does NOT take a lock itself: the caller must hold render_lock(desk_root)
        (flock blocks a second open in the same process, so taking it here would
        deadlock callers that already hold it). A returning retired user gets
        their old index if still free, else the lowest free index; their retired
        row is rewritten in place so each user appears once.
        """
        if not isinstance(user, str) or not USER_RE.match(user):
            raise ValueError(f"invalid username: {user!r}")
        rows = self.rows()
        for r in rows:
            if r.user == user and r.status == "live":
                return r
        taken = {r.index for r in rows if r.status == "live"}
        old = next((r for r in rows if r.user == user), None)
        if old is not None and old.index not in taken and 1 <= old.index <= self.max_sessions:
            index = old.index
        else:
            index = next((i for i in range(1, self.max_sessions + 1) if i not in taken), None)
            if index is None:
                raise RegistryFull(f"all {self.max_sessions} sessions are in use")
        new = Row(user, index, "live")
        if old is None:
            # Rewrite (not append) so a legacy file lacking a final newline
            # cannot have the new row glued onto its last line.
            self._rewrite(rows + [new])
        else:
            self._rewrite([new if r is old else r for r in rows])
        return new

    def retire(self, user: str) -> None:
        """Mark `user`'s row retired. Caller must hold render_lock(desk_root)."""
        rows = self.rows()
        if not any(r.user == user and r.status == "live" for r in rows):
            return
        for r in rows:
            if r.user == user:
                r.status = "retired"
        self._rewrite(rows)

    def _rewrite(self, rows: list[Row]) -> None:
        os.makedirs(self.desk_root, exist_ok=True)
        fd, tmp = tempfile.mkstemp(dir=self.desk_root, prefix=".sessions.tsv.")
        try:
            with os.fdopen(fd, "w") as f:
                for r in rows:
                    f.write(f"{r.user}\t{r.index}\t{r.status}\n")
            os.chmod(tmp, 0o644)
            os.replace(tmp, self.path)
        except BaseException:
            try:
                os.unlink(tmp)
            except FileNotFoundError:
                pass
            raise
