"""Platform definitions: load and validate platforms/<name>/platform.json (stdlib only)."""
import json
import os
import re
from dataclasses import dataclass, field

LOGIN_MODES = ("none", "optional", "required")
POST_ID_SOURCES = {"query": "param", "path": "group", "body": "field"}
DEFAULT_MAX_BODY_BYTES = 16 * 1024 * 1024
DEFAULT_RESPONSE_KEEP = ("has_more", "cursor")


class PlatformError(ValueError):
    """A platform definition is invalid; the message names the offending key path."""


@dataclass(frozen=True)
class ItemKind:
    url: str
    id_re: re.Pattern


@dataclass(frozen=True)
class CaptureRule:
    name: str
    path: re.Pattern
    items: str | None
    id: str | None
    post_id: dict | None
    fields: dict
    query_keep: list
    no_stream: bool
    max_body_bytes: int = DEFAULT_MAX_BODY_BYTES
    response_keep: list = field(default_factory=lambda: list(DEFAULT_RESPONSE_KEEP))


@dataclass(frozen=True)
class Problem:
    code: str
    label: str
    note: bool


@dataclass(frozen=True)
class Platform:
    name: str
    label: str
    zeeschuimer_modules: list
    upload_hint: str
    item_kinds: dict
    default_kind: str
    hosts: list
    open_pattern: re.Pattern
    captures: list
    login: str
    keep_cookies: list
    problems: list
    dir: str


def _need(d, key, typ, where):
    path = f"{where}.{key}" if where else key
    if not isinstance(d, dict) or key not in d:
        raise PlatformError(f"{path}: required")
    if not isinstance(d[key], typ):
        raise PlatformError(f"{path}: must be {typ.__name__}")
    return d[key]


def _regex(value, path):
    try:
        return re.compile(value)
    except re.error as e:
        raise PlatformError(f"{path}: invalid regex ({e})") from None


def _capture(c, i):
    where = f"captures[{i}]"
    name = _need(c, "name", str, where)
    path = _regex(_need(c, "path", str, where), f"{where}.path")
    items = c.get("items")
    cid = c.get("id")
    if items is not None and not isinstance(items, str):
        raise PlatformError(f"{where}.items: must be str")
    if cid is not None and not isinstance(cid, str):
        raise PlatformError(f"{where}.id: must be str")
    if items is not None and not cid:
        raise PlatformError(f"{where}.id: required when items is set")
    post_id = c.get("post_id")
    if post_id is not None:
        src = post_id.get("from") if isinstance(post_id, dict) else None
        if src not in POST_ID_SOURCES:
            raise PlatformError(f"{where}.post_id.from: must be one of {sorted(POST_ID_SOURCES)}")
        key = POST_ID_SOURCES[src]
        if not isinstance(post_id.get(key), str):
            raise PlatformError(f"{where}.post_id.{key}: required when from is {src!r}")
    fields = c.get("fields", {})
    if not isinstance(fields, dict):
        raise PlatformError(f"{where}.fields: must be dict")
    query_keep = c.get("query_keep", [])
    if not isinstance(query_keep, list):
        raise PlatformError(f"{where}.query_keep: must be list")
    response_keep = c.get("response_keep", list(DEFAULT_RESPONSE_KEEP))
    if not isinstance(response_keep, list) or not all(isinstance(k, str) for k in response_keep):
        raise PlatformError(f"{where}.response_keep: must be a list of strings")
    return CaptureRule(
        name=name, path=path, items=items, id=cid, post_id=post_id,
        fields=dict(fields), query_keep=list(query_keep),
        no_stream=bool(c.get("no_stream", False)),
        max_body_bytes=int(c.get("max_body_bytes", DEFAULT_MAX_BODY_BYTES)),
        response_keep=list(response_keep),
    )


def load(path: str) -> Platform:
    try:
        with open(path, encoding="utf-8") as f:
            d = json.load(f)
    except (OSError, ValueError) as e:
        raise PlatformError(f"{path}: cannot read ({e})") from None
    if not isinstance(d, dict):
        raise PlatformError(f"{path}: top level must be an object")

    kinds_raw = _need(d, "item_kinds", dict, "")
    kinds = {}
    for k, v in kinds_raw.items():
        where = f"item_kinds.{k}"
        kinds[k] = ItemKind(url=_need(v, "url", str, where),
                            id_re=_regex(_need(v, "id", str, where), f"{where}.id"))
    default_kind = _need(d, "default_kind", str, "")
    if default_kind not in kinds:
        raise PlatformError(f"default_kind: {default_kind!r} is not in item_kinds")

    login = _need(d, "login", str, "")
    if login not in LOGIN_MODES:
        raise PlatformError(f"login: must be one of {list(LOGIN_MODES)}")

    captures = [_capture(c, i) for i, c in enumerate(_need(d, "captures", list, ""))]

    problems, seen = [], set()
    for i, pr in enumerate(d.get("problems", [])):
        where = f"problems[{i}]"
        code = _need(pr, "code", str, where)
        if code in seen:
            raise PlatformError(f"{where}.code: duplicate code {code!r} in problems")
        seen.add(code)
        problems.append(Problem(code=code, label=_need(pr, "label", str, where),
                                note=bool(pr.get("note", False))))

    return Platform(
        name=_need(d, "name", str, ""),
        label=_need(d, "label", str, ""),
        zeeschuimer_modules=list(_need(d, "zeeschuimer_modules", list, "")),
        upload_hint=_need(d, "upload_hint", str, ""),
        item_kinds=kinds,
        default_kind=default_kind,
        hosts=list(_need(d, "hosts", list, "")),
        open_pattern=_regex(_need(d, "open_pattern", str, ""), "open_pattern"),
        captures=captures,
        login=login,
        keep_cookies=list(d.get("keep_cookies", [])),
        problems=problems,
        dir=os.path.dirname(os.path.abspath(path)),
    )


def load_dir(platforms_dir: str, name: str) -> Platform:
    return load(os.path.join(platforms_dir, name, "platform.json"))


def item_url(p: Platform, kind: str, item_id: str) -> str:
    k = p.item_kinds.get(kind)
    if k is None:
        raise ValueError(f"unknown item kind {kind!r} for platform {p.name}")
    if not isinstance(item_id, str) or not k.id_re.fullmatch(item_id):
        raise ValueError(f"id {item_id!r} does not match {k.id_re.pattern} for kind {kind!r}")
    return k.url.replace("{id}", item_id)


def problems(p: Platform, contact: str) -> list:
    return [Problem(pr.code, pr.label.replace("{contact}", contact), pr.note) for pr in p.problems]


def host_matches(p: Platform, host: str) -> bool:
    host = host.lower().rstrip(".")
    return any(host == h or host.endswith("." + h) for h in p.hosts)
