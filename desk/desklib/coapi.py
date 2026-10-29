"""CO membership from SURF Research Cloud's workspace API (stdlib only)."""
import json
import urllib.request

from .config import DeskConfig
from .registry import USER_RE

TIMEOUT = 10  # seconds; a slow API must not hold the render lock for long


def workspace_info(path: str = "/etc/rsc/workspace.json") -> dict:
    with open(path) as f:
        return json.load(f)


def co_members(info: dict, urlopen=urllib.request.urlopen) -> list[dict]:
    """GET the CO user list: [{"username", "roles": [...], ...}, ...]."""
    req = urllib.request.Request(info["co_user_api_endpoint"], method="GET",
                                 headers={"Authorization": info["co_token"],
                                          "Accept": "application/json"})
    with urlopen(req, timeout=TIMEOUT) as resp:
        data = json.loads(resp.read().decode())
    if not isinstance(data, list):
        raise ValueError("CO API: expected a JSON array")
    return data


class NoMembers(RuntimeError):
    """The CO API answered with nobody while the desk has live sessions; the message is fixed text."""


def guarded_members(fetch, live_count: int) -> dict:
    """fetch() once; an empty answer while `live_count` sessions exist is not believed.

    Every caller that acts on membership (retire, admin sync, the nginx maps) goes through here:
    one flaky CO reply must never revoke every member at once."""
    members = fetch()
    if not members and live_count:
        raise NoMembers("CO API returned no members; refusing to act while sessions exist")
    return members


def membership(members: list[dict], cfg: DeskConfig) -> dict[str, bool]:
    """user -> is_admin, for users holding the collector or the admin role."""
    out = {}
    for m in members:
        user, roles = m.get("username"), m.get("roles")
        if (not isinstance(user, str) or not USER_RE.fullmatch(user)
                or not isinstance(roles, list)):
            continue
        is_admin = cfg.admin_role in roles
        if is_admin or cfg.collector_role in roles:
            out[user] = is_admin
    return out
