"""Generate the Mintlify "API reference" section from the public API spec.

The OpenAPI document embedded in the cloud (av-bridge-cloud/internal/pubapi/
openapi.json) is the single source of truth. This module writes:

  api-reference/openapi.json       customer-facing copy: M.A.R.C.U.S. naming,
                                   a `servers` entry so Mintlify's playground
                                   can send requests, and each operation's
                                   required token scope.
  api-reference/<guide>.mdx        introduction, authentication, pagination,
                                   errors (written here, endpoint and scope
                                   tables generated from the spec and routes).
  api-reference/<tag>/<op>.mdx     one page per operation; Mintlify renders
                                   parameters, schemas and "Try it" from the
                                   spec via the `openapi:` frontmatter.

docs.json must register the spec — "api": {"openapi": "api-reference/openapi.json"} —
or Mintlify renders the endpoint pages with only their titles.

Required scopes are read from the route table in av-bridge-cloud/internal/
api/server.go (pubWrapScope(portalauth.PermX, ...)), and the build fails if
a spec path has no route or a route has no spec entry — so the reference
can't drift from what the server actually serves.
"""
from __future__ import annotations

import json
import re
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent.parent
SPEC = REPO / "av-bridge-cloud" / "internal" / "pubapi" / "openapi.json"
SERVER_GO = REPO / "av-bridge-cloud" / "internal" / "api" / "server.go"
PERMISSIONS_GO = REPO / "av-bridge-cloud" / "internal" / "portalauth" / "permissions.go"
SECTION = "api-reference"
DEFAULT_API_BASE = "https://api.uat.involvecloud.com"

# Sidebar order and icons for the spec's tags.
TAGS = [
    ("Meta", "Token check", "key"),
    ("Devices", "Devices", "display"),
    ("Hierarchy", "Buildings and rooms", "building"),
    ("Assets", "Assets", "boxes-stacked"),
    ("Alerts", "Alerts", "bell"),
    ("Events", "Events", "wave-square"),
    ("Audit", "Audit", "clipboard-list"),
]

# Plain-English description of each token scope, as offered in the portal.
SCOPE_LABEL = {
    "view.dashboard": "Read fleet + devices",
    "view.assets": "Read assets (CMDB)",
    "view.audit": "Read audit log",
}


def slug(s: str) -> str:
    return re.sub(r"[^a-z0-9]+", "-", s.lower()).strip("-")


def route_scopes() -> dict[str, str | None]:
    """Map "GET /pub/v1/x" → required scope (None = any valid token) from server.go."""
    perms = dict(re.findall(r'(Perm\w+)\s*=\s*"([^"]+)"', PERMISSIONS_GO.read_text(encoding="utf-8")))
    src = SERVER_GO.read_text(encoding="utf-8")
    routes: dict[str, str | None] = {}
    for method_path, perm in re.findall(
            r'mux\.Handle\("(GET /pub/v1/[^"]+)",\s*pubWrapScope\(portalauth\.(Perm\w+)', src):
        routes[method_path] = perms[perm]
    for method_path in re.findall(r'mux\.Handle\("(GET /pub/v1/[^"]+)",\s*pubWrap\(', src):
        routes[method_path] = None
    return routes


def customer_spec(spec: dict, api_base: str, scopes: dict[str, str | None]) -> dict:
    s = json.loads(json.dumps(spec))
    info = s["info"]
    info["title"] = "M.A.R.C.U.S. Public API"
    for k in ("summary", "description"):
        if k in info:
            info[k] = info[k].replace("av-bridge", "M.A.R.C.U.S.")
    # No per-token rate limiting is enforced, so don't promise one.
    info["description"] = re.sub(r"Rate limits are per-token; ", "", info["description"])
    s["servers"] = [{"url": api_base.rstrip("/"), "description": "M.A.R.C.U.S. Cloud (UK)"}]
    # Event ids are bigserial numbers sent as strings, not UUIDs; strict
    # generated clients would reject them if the schema said uuid.
    ev = s.get("components", {}).get("schemas", {}).get("Event", {}).get("properties", {})
    if ev.get("id", {}).get("format") == "uuid":
        ev["id"] = {"type": "string", "description": "Event id: a number, sent as a string. Increases over time."}
    for path, ops in s["paths"].items():
        for method, op in ops.items():
            scope = scopes.get(f"{method.upper()} {path}")
            note = f"Requires a token with the `{scope}` scope." if scope else "Any valid token."
            if scope and f"`{scope}`" not in op.get("description", ""):
                op["description"] = (op.get("description", "") + "\n\n" + note).strip()
            op["x-required-scope"] = scope or "any"
    return s


def operations(spec: dict) -> list[dict]:
    out = []
    for path, ops in spec["paths"].items():
        for method, op in ops.items():
            out.append({"method": method.upper(), "path": path, "op": op,
                        "tag": (op.get("tags") or ["Other"])[0]})
    return out


def check_routes(spec: dict, scopes: dict[str, str | None]) -> None:
    documented = {f"{o['method']} {o['path']}" for o in operations(spec)}
    served = set(scopes)
    missing_doc = sorted(served - documented)
    missing_route = sorted(documented - served)
    if missing_doc or missing_route:
        raise SystemExit("api_docs: spec and server routes disagree — "
                         f"undocumented routes {missing_doc}, documented but not served {missing_route}")
    unknown_tags = sorted({o["tag"] for o in operations(spec)} - {t for t, _, _ in TAGS})
    if unknown_tags:
        raise SystemExit(f"api_docs: add sidebar entries to TAGS for {unknown_tags}")


def guide_pages(spec: dict, ops: list[dict], scopes: dict[str, str | None], api_base: str,
                page_path: dict[str, str]) -> dict[str, tuple[dict, str]]:
    base = api_base.rstrip("/")
    by_tag: dict[str, list[dict]] = {}
    for o in ops:
        by_tag.setdefault(o["tag"], []).append(o)

    rows = []
    for tag, label, _ in TAGS:
        for o in by_tag.get(tag, []):
            key = f"{o['method']} {o['path']}"
            scope = scopes.get(key)
            rows.append(f"| [`{o['method']} {o['path']}`](/{page_path[key]}) | {o['op'].get('summary', '')} | "
                        f"{'`' + scope + '`' if scope else 'Any'} |")
    endpoint_table = "\n".join(["| Endpoint | What it does | Scope |", "|---|---|---|", *rows])

    used = sorted({s for s in scopes.values() if s})
    scope_rows = []
    for s in used:
        tags = sorted({o["tag"] for o in ops if scopes.get(f"{o['method']} {o['path']}") == s},
                      key=[t for t, _, _ in TAGS].index)
        labels = ", ".join(next(l for t, l, _ in TAGS if t == tag) for tag in tags)
        scope_rows.append(f"| `{s}` | {SCOPE_LABEL.get(s, '')} | {labels} |")
    scope_table = "\n".join(["| Scope | Shown in the portal as | Grants |", "|---|---|---|", *scope_rows])

    intro = f"""The M.A.R.C.U.S. Public API gives other systems read-only access to your estate: sync devices and assets into a CMDB, mirror alerts into an incident tool, feed dashboards, or send the audit trail to a SIEM.

Every request is scoped to the customer the token belongs to, and the API never changes anything — it only reads.

## Base URL

```
{base}
```

All endpoints are under `/pub/v1`. Requests and responses are JSON over HTTPS.

## Make your first call

<Steps>
  <Step title="Create a token">
    In the portal, go to **Settings → API tokens** and select **New token**. Choose the scopes it needs and an expiry. Copy the token when it's shown: it isn't displayed again. See [Authentication](/{SECTION}/authentication).
  </Step>
  <Step title="Check it works">
    ```bash
    curl -H "Authorization: Bearer avb_..." {base}/pub/v1/ping
    ```
    The reply names the customer and the token's scopes.
  </Step>
  <Step title="Read some data">
    ```bash
    curl -H "Authorization: Bearer avb_..." "{base}/pub/v1/devices?status=offline"
    ```
  </Step>
</Steps>

## Endpoints

{endpoint_table}

<Tip>
The OpenAPI 3.1 document is also served by the API itself at `{base}/pub/v1/openapi.json`, for importing into Postman, Insomnia or a code generator.
</Tip>
"""

    auth = f"""Every request needs an API token in the `Authorization` header:

```
Authorization: Bearer avb_<prefix>_<secret>
```

Tokens in the query string are not accepted, because URLs end up in logs.

## Create a token

1. In the portal, go to **Settings → API tokens** and select **New token**. You need the **Manage API tokens** permission.
2. Give it a name that says what uses it, such as *ServiceNow CMDB sync*.
3. Choose its scopes (below) and when it expires.
4. Copy the token. It's shown once only; M.A.R.C.U.S. keeps just a hash of it.

The token's prefix (`avb_1a2b3c4d`) is shown in the token list, so you can tell tokens apart and revoke the right one.

## Scopes

A token can only read what its scopes allow. Give each integration only the scopes it needs.

{scope_table}

`/pub/v1/ping` works with any valid token.

## Revoke or replace a token

Revoke a token from **Settings → API tokens** and it stops working immediately. To rotate, create the new token, update the integration, then revoke the old one.

## When a request is refused

| Status | Meaning |
|---|---|
| `401 unauthorized` | No token, or the token is wrong, expired or revoked. |
| `403 forbidden` | The token is valid but doesn't have the scope this endpoint needs. |
"""

    pagination = f"""List endpoints return a page of results and a cursor for the next page:

```json
{{
  "data": [ ... ],
  "next_cursor": "eyJ0cyI6IjIwMjYtMTAtMDVUMDk6MTI6MDBaIiwiaWQiOiIuLi4ifQ"
}}
```

- `limit` sets the page size: 100 by default, 500 at most.
- When `next_cursor` is `null`, there are no more results.
- Otherwise, pass it back unchanged as `cursor`, keeping the same filters, to get the next page.

Treat the cursor as an opaque string. Its contents may change; only its use as `cursor` is part of the contract.

Results are newest first (for devices, most recently seen first), and the cursor keeps paging stable while new records arrive: you won't see an item twice or miss one.

## Example: fetch every device

```bash
cursor=""
while :; do
  page=$(curl -s -H "Authorization: Bearer $TOKEN" \\
    "{base}/pub/v1/devices?limit=500${{cursor:+&cursor=$cursor}}")
  echo "$page" | jq -c '.data[]'
  cursor=$(echo "$page" | jq -r '.next_cursor // empty')
  [ -z "$cursor" ] && break
done
```

## Polling

For a feed such as alerts, events or the audit trail, poll on a schedule and stop when you reach records you've already processed. Once a minute is ample for most integrations.

Paginated endpoints: devices, a device's events, assets, alerts, events and audit. Buildings and rooms return a complete list in one response.
"""

    errors = """Errors use the same shape on every endpoint:

```json
{
  "error": {
    "code": "not_found",
    "message": "device not found"
  }
}
```

`code` is stable and safe to match on; `message` is for people and may be reworded.

| Status | `code` | When |
|---|---|---|
| 400 | `bad_request` | A parameter is invalid, such as a malformed `cursor` or a `since` that isn't an RFC 3339 timestamp. |
| 401 | `unauthorized` | No token, or the token is wrong, expired or revoked. |
| 403 | `forbidden` | The token doesn't have the scope this endpoint needs. |
| 404 | `not_found` | The record doesn't exist, or belongs to another customer. |
| 500 | `internal_error` | Something went wrong on our side. Retry later; if it persists, contact support with the time and endpoint. |

A filter that matches nothing returns `200` with an empty `data` list, not `404`.
"""

    return {
        "introduction": ({"title": "Public API", "sidebarTitle": "Introduction",
                          "description": "Read-only access to your estate for CMDBs, incident tools, dashboards and SIEMs.",
                          "icon": "code"}, intro),
        "authentication": ({"title": "Authentication", "description": "API tokens, scopes and revocation.",
                            "icon": "key"}, auth),
        "pagination": ({"title": "Pagination", "description": "Cursor-based paging through list endpoints.",
                        "icon": "list"}, pagination),
        "errors": ({"title": "Errors", "description": "Error responses and status codes.",
                    "icon": "triangle-exclamation"}, errors),
    }


def build(out_dir: Path, frontmatter, api_base: str = DEFAULT_API_BASE) -> tuple[list[Path], list[dict]]:
    """Write the section; returns (files written, the API reference tab's nav groups)."""
    spec = json.loads(SPEC.read_text(encoding="utf-8"))
    scopes = route_scopes()
    check_routes(spec, scopes)
    pub = customer_spec(spec, api_base, scopes)
    ops = operations(pub)

    base = out_dir / SECTION
    base.mkdir(parents=True, exist_ok=True)
    written: list[Path] = []
    spec_path = base / "openapi.json"
    spec_path.write_text(json.dumps(pub, indent=2, ensure_ascii=False) + "\n", encoding="utf-8", newline="\n")
    written.append(spec_path)

    page_path: dict[str, str] = {}
    tag_pages: dict[str, list[str]] = {}
    for o in ops:
        key = f"{o['method']} {o['path']}"
        rel = f"{SECTION}/{slug(o['tag'])}/{slug(o['op'].get('summary') or o['path'])}"
        page_path[key] = rel
        tag_pages.setdefault(o["tag"], []).append(rel)
        meta = {"title": o["op"].get("summary", key), "openapi": f"/{SECTION}/openapi.json {key}"}
        dest = out_dir / f"{rel}.mdx"
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(frontmatter(meta), encoding="utf-8", newline="\n")
        written.append(dest)

    for name, (meta, body) in guide_pages(pub, ops, scopes, api_base, page_path).items():
        dest = base / f"{name}.mdx"
        dest.write_text(frontmatter(meta) + body, encoding="utf-8", newline="\n")
        written.append(dest)

    groups = [{"group": "Getting started", "icon": "rocket",
               "pages": [f"{SECTION}/introduction", f"{SECTION}/authentication",
                         f"{SECTION}/pagination", f"{SECTION}/errors"]}]
    groups += [{"group": label, "icon": icon, "pages": tag_pages[tag]}
               for tag, label, icon in TAGS if tag in tag_pages]
    return written, groups
