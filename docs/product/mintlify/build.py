#!/usr/bin/env python3
"""Convert M.A.R.C.U.S. doc-pack Markdown into Mintlify-ready MDX.

The .md files in docs/product/ stay the single source of truth (they also
feed the Word generator). This script writes MDX that Mintlify accepts:

  * Mintlify frontmatter (title / sidebarTitle / description / icon);
    source-only keys (vendor, audience...) are dropped.
  * HTML comments, the leading H1 + strapline and horizontal rules removed.
  * "> **Label.** text" blockquotes become <Note>/<Tip>/<Info>/<Warning>.
  * Bare "<", "{" and "}" outside code escaped so MDX doesn't read JSX.

Two kinds of document:

  PAGES   one .md -> one Mintlify page.
  GUIDES  one .md -> a section of pages. Each numbered "## N. Title"
          becomes its own page (headings promoted one level), "§N"
          cross-references become links between pages, and the nav is
          nested by the "nav" group of each section. Unnumbered "##"
          sections (e.g. Related documents) are Word-only and dropped.

Usage:
  python docs/product/mintlify/build.py                   # everything -> ./out
  python docs/product/mintlify/build.py deployment-guide
  python docs/product/mintlify/build.py devices           # Supported devices (from the adapter catalogue)
  python docs/product/mintlify/build.py api               # API reference (from the public API spec)
  python docs/product/mintlify/build.py --api-base https://api.example.com   # server for "Try it"
  python docs/product/mintlify/build.py --out C:/path/to/mintlify-repo
"""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
import time
import urllib.parse
from pathlib import Path

HERE = Path(__file__).resolve().parent
SRC_DIR = HERE.parent

# ---------------------------------------------------------------------------
# Content map
# ---------------------------------------------------------------------------

# Single-page documents. Add the rest of the doc pack here as each is ready.
PAGES: dict[str, dict] = {}

# Hand-written Mintlify pages under static/<folder>/, shown as sidebar
# groups after the Introduction, in this order. Every .mdx in a listed
# folder must appear here (the build checks), so a new page can't be
# copied without a place in the navigation.
STATIC_GROUPS: list[dict] = [
    {"group": "Using the portal", "icon": "computer-mouse", "folder": "portal",
     "pages": ["getting-around", "overview-and-map", "dashboards", "devices", "alerts",
               "reports", "room-readiness", "test-routines"]},
    {"group": "Administration", "icon": "user-gear", "folder": "admin",
     "pages": ["users-and-access", "roles", "single-sign-on", "notifications",
               "api-tokens", "branding", "audit-log"]},
]

# Multi-page guides. Section numbers match the "## N." headings in the .md.
GUIDES: dict[str, dict] = {
    "deployment-guide": {
        "dir": "deploy",
        "group": "Deploy the Collector",
        "icon": "rocket-launch",
        "sections": {
            1: dict(slug="overview", title="Deploy the Collector", sidebarTitle="Overview",
                    icon="compass",
                    description="How the M.A.R.C.U.S. Collector fits your estate, the two "
                                "decisions that shape a deployment, and where to start.",
                    cards=[("Plan", "sitemap", 2, "Choose a topology, check the network and size the host."),
                           ("Install", "download", 5, "Step-by-step recipes for Linux, Windows, Docker, Kubernetes and managed platforms."),
                           ("Operate", "heart-pulse", 11, "Update, monitor and back up your Collectors.")],
                    drop_sections=["How the guide is organised"]),
            2: dict(slug="topologies", title="Choose a topology", nav="Plan", icon="sitemap",
                    description="Per-site, central, hybrid or cloud-hosted: pick the Collector "
                                "topology that fits your estate."),
            3: dict(slug="network-requirements", title="Network requirements", nav="Plan",
                    icon="network-wired",
                    description="The single outbound firewall rule, device protocols and the "
                                "optional local port."),
            4: dict(slug="sizing", title="Size the host", nav="Plan", icon="gauge",
                    description="Compute and bandwidth per Collector, and when to add another."),
            5: dict(slug="install-linux", title="Install on Linux", sidebarTitle="Linux",
                    nav="Install", icon="linux",
                    description="Install the Collector as a systemd service with one command."),
            6: dict(slug="install-windows", title="Install on Windows Server",
                    sidebarTitle="Windows Server", nav="Install", icon="windows",
                    description="Install the Collector as a Windows service from PowerShell."),
            7: dict(slug="docker", title="Run with Docker", sidebarTitle="Docker",
                    nav="Install", icon="docker",
                    description="Run the Collector container with Docker Compose or docker run."),
            8: dict(slug="kubernetes", title="Run on Kubernetes", sidebarTitle="Kubernetes",
                    nav="Install", icon="dharmachakra", tabs=True,
                    description="Deploy the Collector on EKS, AKS, GKE or an on-premises cluster."),
            9: dict(slug="managed-containers", title="Run on a managed container platform",
                    sidebarTitle="Managed containers", nav="Install", icon="cloud", tabs=True,
                    description="Run the Collector on AWS ECS Fargate, Azure Container Instances "
                                "or Google Compute Engine."),
            10: dict(slug="configuration", title="Secure the configuration",
                     sidebarTitle="Configuration & secrets", nav="Install", icon="key",
                     description="What the Collector configuration holds, how to protect the HMAC "
                                 "key and how to rotate it."),
            11: dict(slug="updates", title="Update the Collector", sidebarTitle="Updates",
                     nav="Operate", icon="arrows-rotate",
                     description="Update Collectors from the portal, automatically or by hand, and the "
                                 "version compatibility commitment."),
            12: dict(slug="monitoring", title="Monitor health and logs",
                     sidebarTitle="Health & logs", nav="Operate", icon="heart-pulse",
                     description="Local health checks, Prometheus metrics, portal status and logs."),
            13: dict(slug="backup", title="Outages, backup and replacement",
                     sidebarTitle="Outages & replacement", nav="Operate", icon="box-archive",
                     description="How the Collector rides out a cloud outage, what to back up, "
                                 "how to replace a failed Collector, and automatic failover "
                                 "with a standby."),
        },
    },
}

# Blockquote label -> Mintlify callout component. First match wins.
CALLOUTS = [
    (re.compile(r"^\*\*(best for|tip)\b", re.I), "Tip"),
    (re.compile(r"^\*\*(warning|important|caution)\b", re.I), "Warning"),
    (re.compile(r"^\*\*uk-hosted\b", re.I), "Info"),
]
DEFAULT_CALLOUT = "Note"
COMPONENTS = r"(Note|Tip|Info|Warning|Tabs|Tab|Card|CardGroup)"

FENCE = re.compile(r"^\s*(```|~~~)")
TAB_ITEM = re.compile(r"^- \*\*(.+?):\*\*\s*(.*)$")
SECTION = re.compile(r"^## (\d+)\.\s+(.*)$")


# ---------------------------------------------------------------------------
# Line-level helpers
# ---------------------------------------------------------------------------

def split_frontmatter(text: str) -> tuple[str, str]:
    if text.startswith("---\n"):
        end = text.find("\n---\n", 4)
        if end != -1:
            return text[4:end], text[end + 5:]
    return "", text


def yaml_str(v: str) -> str:
    return json.dumps(v, ensure_ascii=False)  # JSON strings are valid YAML


def escape_inline(line: str, xref=None) -> str:
    """Escape MDX-significant characters outside `inline code` spans and
    turn §N references into links when an xref resolver is given."""
    parts = re.split(r"(`+[^`]*`+)", line)
    out = []
    for p in parts:
        if p.startswith("`"):
            out.append(p)
            continue
        p = p.replace("{", "\\{").replace("}", "\\}")
        p = re.sub(rf"<(?!/?{COMPONENTS}\b)", "&lt;", p)
        if xref:
            # "(§5)" -> "(see [Install on Linux](...))"; bare "§5" -> link.
            p = re.sub(r"\((§\d+(?:\s*–\s*§\d+)?)\)",
                       lambda m: "(see " + m.group(1) + ")", p)
            p = re.sub(r"§(\d+)(?:\s*–\s*§(\d+))?", xref, p)
        out.append(p)
    return "".join(out)


def callout_for(body: str) -> str:
    for rx, comp in CALLOUTS:
        if rx.search(body):
            return comp
    return DEFAULT_CALLOUT


def frontmatter(meta: dict) -> str:
    fm = ["---"]
    for key in ("title", "sidebarTitle", "description", "icon", "openapi"):
        if meta.get(key):
            fm.append(f"{key}: {yaml_str(meta[key])}")
    fm.append("---")
    return "\n".join(fm) + "\n\n"


# ---------------------------------------------------------------------------
# Body conversion
# ---------------------------------------------------------------------------

def convert(body: str, *, promote: int = 0, tabs: bool = False, xref=None) -> str:
    body = re.sub(r"<!--.*?-->\s*", "", body, flags=re.S)
    lines = body.splitlines()
    out: list[str] = []
    in_code = False
    dropped_h1 = False
    i = 0
    while i < len(lines):
        line = lines[i]

        if FENCE.match(line):
            in_code = not in_code
            out.append(line)
            i += 1
            continue
        if in_code:
            out.append(line)
            i += 1
            continue

        # Leading H1 and the strapline directly under it.
        if not dropped_h1 and line.startswith("# "):
            dropped_h1 = True
            i += 1
            while i < len(lines) and not lines[i].strip():
                i += 1
            if i < len(lines) and lines[i].startswith("**Managed ·"):
                i += 1
            continue

        if line.strip() == "---":
            i += 1
            continue

        if promote and re.match(r"^#{2,6} ", line):
            hashes, rest = line.split(" ", 1)
            line = "#" * max(2, len(hashes) - promote) + " " + rest

        # "- **Label:** text" bullet runs -> <Tabs> (only where enabled).
        if tabs and TAB_ITEM.match(line):
            items = []
            while i < len(lines) and TAB_ITEM.match(lines[i]):
                label, text = TAB_ITEM.match(lines[i]).groups()
                items.append((label, text))
                i += 1
            if len(items) > 1:
                out.append("<Tabs>")
                for label, text in items:
                    out += [f"  <Tab title={yaml_str(label)}>",
                            "    " + escape_inline(text[0].upper() + text[1:], xref),
                            "  </Tab>"]
                out.append("</Tabs>")
            else:
                label, text = items[0]
                out.append(escape_inline(f"- **{label}:** {text}", xref))
            continue

        if line.startswith(">"):
            block = []
            while i < len(lines) and lines[i].startswith(">"):
                block.append(lines[i][1:].lstrip())
                i += 1
            text = " ".join(b for b in block if b).strip()
            comp = callout_for(text)
            out += [f"<{comp}>", escape_inline(text, xref), f"</{comp}>"]
            continue

        out.append(escape_inline(line, xref))
        i += 1

    text = "\n".join(out)
    return re.sub(r"\n{3,}", "\n\n", text).strip() + "\n"


def drop_subsections(body: str, titles: list[str]) -> str:
    """Remove '### Title' subsections (up to the next ### or ##)."""
    for t in titles:
        body = re.sub(rf"(?ms)^### {re.escape(t)}\n.*?(?=^##|\Z)", "", body)
    return body


# ---------------------------------------------------------------------------
# Builders
# ---------------------------------------------------------------------------

def build_page(slug: str, meta: dict, out_dir: Path) -> list[Path]:
    _, body = split_frontmatter((SRC_DIR / f"{slug}.md").read_text(encoding="utf-8"))
    dest = out_dir / f"{slug}.mdx"
    dest.parent.mkdir(parents=True, exist_ok=True)
    dest.write_text(frontmatter(meta) + convert(body), encoding="utf-8", newline="\n")
    return [dest]


def split_sections(body: str) -> dict[int, str]:
    """Map section number -> body text (without its '## N.' heading)."""
    sections: dict[int, list[str]] = {}
    current = None
    in_code = False
    for line in body.splitlines():
        if FENCE.match(line):
            in_code = not in_code
        m = None if in_code else SECTION.match(line)
        if m:
            current = int(m.group(1))
            sections[current] = []
            continue
        if not in_code and line.startswith("## "):
            current = None  # unnumbered section: Word-only
            continue
        if current is not None:
            sections[current].append(line)
    return {n: "\n".join(v) for n, v in sections.items()}


def build_guide(name: str, guide: dict, out_dir: Path) -> list[Path]:
    _, body = split_frontmatter((SRC_DIR / f"{name}.md").read_text(encoding="utf-8"))
    parts = split_sections(body)
    cfg = guide["sections"]
    missing = sorted(set(parts) ^ set(cfg))
    if missing:
        raise SystemExit(f"{name}: sections {missing} are in only one of the .md "
                         f"and GUIDES[{name!r}]; keep them in step")

    def href(n: int) -> str:
        return f"/{guide['dir']}/{cfg[n]['slug']}"

    def xref(m: re.Match) -> str:
        a, b = int(m.group(1)), m.group(2)
        if a not in cfg:
            return m.group(0)
        link = f"[{cfg[a]['title']}]({href(a)})"
        if b and int(b) in cfg:
            link += f" to [{cfg[int(b)]['title']}]({href(int(b))})"
        return link

    written = []
    for n, meta in cfg.items():
        text = drop_subsections(parts[n], meta.get("drop_sections", []))
        mdx = convert(text, promote=1, tabs=meta.get("tabs", False), xref=xref)
        if meta.get("cards"):
            cards = ['<CardGroup cols={3}>']
            for title, icon, target, blurb in meta["cards"]:
                cards += [f'  <Card title={yaml_str(title)} icon={yaml_str(icon)} '
                          f'href={yaml_str(href(target))}>',
                          f"    {escape_inline(blurb)}",
                          "  </Card>"]
            cards.append("</CardGroup>")
            mdx += "\n## Where to start\n\n" + "\n".join(cards) + "\n"
        dest = out_dir / guide["dir"] / f"{meta['slug']}.mdx"
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(frontmatter(meta) + mdx, encoding="utf-8", newline="\n")
        written.append(dest)
    return written


def guide_nav(guide: dict) -> dict:
    pages: list = []
    groups: dict[str, list[str]] = {}
    for meta in guide["sections"].values():
        path = f"{guide['dir']}/{meta['slug']}"
        if meta.get("nav"):
            if meta["nav"] not in groups:
                groups[meta["nav"]] = []
                pages.append({"group": meta["nav"], "pages": groups[meta["nav"]]})
            groups[meta["nav"]].append(path)
        else:
            pages.append(path)
    return {"group": guide["group"], "icon": guide.get("icon"), "pages": pages}


_DOWNLOAD_LINK = re.compile(r"\[([^\]]+)\]\(/(files/[^)\s]+\.csv)\)")


def inline_downloads(mdx: str, static: Path) -> str:
    """Rewrites [text](/files/x.csv) as a link that carries the file as a
    data: URL with a download attribute, so it downloads on any Mintlify plan
    (the plain path only works on Enterprise). A UTF-8 byte-order mark is
    added so Excel opens accented characters correctly; the asset import
    accepts it."""
    def repl(m: re.Match) -> str:
        path = static / m.group(2)
        if not path.exists():
            raise SystemExit(f"{m.group(2)} is linked but missing from static/")
        data = urllib.parse.quote("\ufeff" + path.read_text(encoding="utf-8"), safe="")
        return f'<a href="data:text/csv;charset=utf-8,{data}" download="{path.name}">{m.group(1)}</a>'
    return _DOWNLOAD_LINK.sub(repl, mdx)


# Every page this build writes gets a footer, "Updated 23 Oct 2026 14:52 ·
# page a1b2c3d": when the page's content last changed, and a fingerprint of
# that content. Both stay the same while the page does (the date is carried
# over from the previous build's docs-build.json), so an unchanged page is
# byte-for-byte the same and a publish only touches pages that changed.
# Compare a live page's footer with out/docs-build.json to see whether it's
# the latest. A page with no footer came from static/ rather than out/.
_STAMP = re.compile(r"\n*<p className=\"docs-build-stamp\"[^\n]*</p>\n*\Z")


def stamp_pages(out_dir: Path, since: float) -> int:
    manifest_path = out_dir / "docs-build.json"
    previous: dict[str, dict] = {}
    if manifest_path.exists():
        for slug, v in json.loads(manifest_path.read_text(encoding="utf-8")).get("pages", {}).items():
            if isinstance(v, dict):
                previous[slug] = v
    now = time.strftime("%d %b %Y %H:%M")
    pages: dict[str, dict] = {}
    for path in sorted(out_dir.rglob("*.mdx")):
        if path.stat().st_mtime < since:
            continue  # not written by this build (a page from an older one)
        text = _STAMP.sub("\n", path.read_text(encoding="utf-8")).rstrip("\n") + "\n"
        page = hashlib.sha256(text.encode("utf-8")).hexdigest()[:7]
        slug = path.relative_to(out_dir).with_suffix("").as_posix()
        prev = previous.get(slug, {})
        updated = prev["updated"] if prev.get("page") == page else now
        pages[slug] = {"page": page, "updated": updated}
        path.write_text(
            text + f'\n<p className="docs-build-stamp" style={{{{fontSize: "0.75rem", opacity: 0.6, marginTop: "3rem"}}}}>'
            f"Updated {updated} · page {page}</p>\n",
            encoding="utf-8", newline="\n")
    manifest_path.write_text(json.dumps({"built": now, "pages": pages}, indent=2) + "\n", encoding="utf-8")
    return len(pages)


# ── Sync into the Mintlify repo ───────────────────────────────────────────────
#
# The published site builds from a separate repo (path in .mintlify-repo).
# After a build, everything in out/ is copied there, except the
# build's own working files; the repo's docs.json keeps its settings and
# only gets its navigation replaced. .marcus-build.json in the repo records
# what the sync wrote, with a hash of each file, so the next sync can:
#  - leave alone a page edited in the repo since (say on GitHub), and say
#    so: bring the change into static/ or the source documents, or --force;
#  - delete a page it wrote that the build no longer produces, but never a
#    file it didn't write (README, index.mdx, logos...).
# Nothing is committed or pushed: review and push from the repo.

REPO_CONFIG = HERE / ".mintlify-repo"
REPO_MANIFEST = ".marcus-build.json"
# out/ files that are the build's own, not site content.
NOT_SYNCED = {"docs.json", "docs-navigation.json", "docs-build.json"}


def configured_repo() -> Path | None:
    if not REPO_CONFIG.exists():
        return None
    line = REPO_CONFIG.read_text(encoding="utf-8").strip()
    return Path(line) if line else None


def _sha(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def sync_repo(out_dir: Path, repo: Path, force: bool, since: float) -> int:
    if not (repo / "docs.json").exists():
        print(f"{repo} doesn't look like a Mintlify repo (no docs.json); not synced", file=sys.stderr)
        return 1
    manifest_path = repo / REPO_MANIFEST
    previous: dict[str, str] = {}
    if manifest_path.exists():
        previous = json.loads(manifest_path.read_text(encoding="utf-8")).get("files", {})

    written: dict[str, str] = {}
    copied, unchanged, kept = [], 0, []
    for src in sorted(p for p in out_dir.rglob("*") if p.is_file()):
        rel = src.relative_to(out_dir).as_posix()
        # Only what this build wrote: out/ can hold pages from older builds
        # (a renamed API endpoint, say), which mustn't be published.
        if rel in NOT_SYNCED or src.stat().st_mtime < since:
            continue
        dest = repo / rel
        new = _sha(src)
        if dest.exists():
            current = _sha(dest)
            if current == new:
                written[rel] = new
                unchanged += 1
                continue
            # Changed in the repo since we last wrote it: someone edited it there.
            if rel in previous and current != previous[rel] and not force:
                kept.append(rel)
                written[rel] = previous[rel]
                continue
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(src.read_bytes())
        written[rel] = new
        copied.append(rel)

    # Pages a previous sync wrote that the build no longer produces, unless
    # they've been edited in the repo since.
    removed = []
    for rel, h in previous.items():
        if rel in written:
            continue
        dest = repo / rel
        if dest.exists() and _sha(dest) == h:
            dest.unlink()
            removed.append(rel)

    # docs.json: keep everything, replace the navigation.
    docs_json = repo / "docs.json"
    cfg = json.loads(docs_json.read_text(encoding="utf-8"))
    nav = json.loads((out_dir / "docs-navigation.json").read_text(encoding="utf-8"))
    nav_changed = cfg.get("navigation") != nav
    if nav_changed:
        cfg["navigation"] = nav
        docs_json.write_text(json.dumps(cfg, indent=2, ensure_ascii=False) + "\n", encoding="utf-8", newline="\n")

    build = json.loads((out_dir / "docs-build.json").read_text(encoding="utf-8"))
    manifest_path.write_text(json.dumps({"built": build["built"], "files": written}, indent=2) + "\n",
                             encoding="utf-8", newline="\n")
    ignore = repo / ".mintignore"
    lines = ignore.read_text(encoding="utf-8").splitlines() if ignore.exists() else []
    if REPO_MANIFEST not in lines:
        ignore.write_text("\n".join(lines + ["", "# The av-bridge docs build's record of the files it manages", REPO_MANIFEST]) + "\n",
                          encoding="utf-8", newline="\n")

    print(f"\nsynced into {repo}: {len(copied)} updated, {unchanged} unchanged, {len(removed)} removed"
          + (", docs.json navigation updated" if nav_changed else ""))
    for rel in copied:
        print(f"  updated {rel}")
    for rel in removed:
        print(f"  removed {rel}")
    if kept:
        print("\nedited in the repo since the last sync, so left as they are (bring the change into the"
              " av-bridge docs, or re-run with --force to overwrite):")
        for rel in kept:
            print(f"  {rel}")
    print(f"\nreview and publish:  cd \"{repo}\"  &&  git status  &&  git add -A  &&  git commit  &&  git push")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("docs", nargs="*", help="documents to build (default: all)")
    ap.add_argument("--out", type=Path, default=HERE / "out")
    ap.add_argument("--repo", type=Path, default=None,
                    help="Mintlify docs repo to sync the build into (default: the path in .mintlify-repo)")
    ap.add_argument("--no-sync", action="store_true", help="build into --out only; don't sync the Mintlify repo")
    ap.add_argument("--force", action="store_true",
                    help="sync over pages edited in the Mintlify repo since the last sync")
    ap.add_argument("--api-base", default=None,
                    help="API server shown in the API reference and used by its playground "
                         "(default: the UAT API)")
    args = ap.parse_args()
    # A second's slack: some filesystems keep modification times to the second.
    started = time.time() - 1

    # "devices" is the Supported devices section, generated from the adapter
    # catalogue by adapter_docs.py rather than from a Markdown source.
    # "api" is the API reference, generated from the public API's OpenAPI
    # spec and route table by api_docs.py.
    known = {**PAGES, **GUIDES, "devices": None, "api": None}
    names = args.docs or list(known)
    unknown = [n for n in names if n not in known]
    if unknown:
        print(f"unknown document(s): {', '.join(unknown)}; known: {', '.join(known)}",
              file=sys.stderr)
        return 1

    # Navigation is two sections. Documentation starts with the hand-written
    # Introduction (static/quickstart.mdx); the API reference tab is filled
    # by api_docs. docs-navigation.json is the whole "navigation" value.
    doc_groups = [{"group": "Introduction", "icon": "book-open", "pages": ["quickstart", "onboarding"]}]
    for g in STATIC_GROUPS:
        folder = HERE / "static" / g["folder"]
        present = sorted(p.stem for p in folder.glob("*.mdx")) if folder.exists() else []
        listed = sorted(g["pages"])
        if present != listed:
            print(f"static/{g['folder']}: pages on disk {present} don't match STATIC_GROUPS {listed}",
                  file=sys.stderr)
            return 1
        doc_groups.append({"group": g["group"], "icon": g["icon"],
                           "pages": [f"{g['folder']}/{p}" for p in g["pages"]]})
    api_groups: list[dict] = []
    nav = doc_groups
    for n in names:
        if n == "devices":
            import adapter_docs
            files, group = adapter_docs.build(args.out, convert, frontmatter, yaml_str)
            nav.append(group)
        elif n == "api":
            import api_docs
            files, api_groups = api_docs.build(args.out, frontmatter, args.api_base or api_docs.DEFAULT_API_BASE)
            # Endpoint pages render empty unless docs.json registers the spec.
            print('note: docs.json needs  "api": {"openapi": "api-reference/openapi.json"}'
                  '  or the API reference pages show only their titles')
        elif n in GUIDES:
            files = build_guide(n, GUIDES[n], args.out)
            nav.append(guide_nav(GUIDES[n]))
        else:
            files = build_page(n, PAGES[n], args.out)
            nav.append({"group": PAGES[n].get("group", "Documentation"), "pages": [n]})
        for f in files:
            print(f"wrote {f}")

    # Anchors rather than tabs: the willow theme shows tabs as a dropdown,
    # anchors as a row of links at the top of the sidebar.
    anchors = [{"anchor": "Documentation", "icon": "book-open", "groups": doc_groups}]
    if api_groups:
        anchors.append({"anchor": "API reference", "icon": "code", "groups": api_groups})
    nav_file = args.out / "docs-navigation.json"
    nav_file.write_text(json.dumps({"anchors": anchors}, indent=2) + "\n", encoding="utf-8")
    print(f"wrote {nav_file}  (paste into docs.json -> navigation)")

    # Hand-written Mintlify-only pages (no Word equivalent), e.g. quickstart.
    static = HERE / "static"
    # Downloads the pages link to, such as the asset import template, are
    # copied too, but Mintlify only serves .csv files on its Enterprise plan;
    # elsewhere the link 404s. So a page's links to /files/*.csv are also
    # rewritten to carry the file itself (inline_downloads), which works on
    # any plan.
    for src in sorted([*static.rglob("*.mdx"), *static.rglob("*.csv")]):
        dest = args.out / src.relative_to(static)
        dest.parent.mkdir(parents=True, exist_ok=True)
        if src.suffix == ".mdx":
            dest.write_text(inline_downloads(src.read_text(encoding="utf-8"), static), encoding="utf-8")
        else:
            dest.write_bytes(src.read_bytes())
        print(f"wrote {dest}")

    # Brand assets (generated by logo/make_logo.py) — docs.json points at
    # /images/marcus-{light,dark}.svg and /images/favicon.svg.
    logo_dir = HERE / "logo"
    for name in ("marcus-light.svg", "marcus-dark.svg", "favicon.svg"):
        src, dest = logo_dir / name, args.out / "images" / name
        if src.exists():
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_bytes(src.read_bytes())
            print(f"wrote {dest}")
    n = stamp_pages(args.out, started)
    print(f"stamped {n} pages (out/docs-build.json lists each page's fingerprint)")

    repo = args.repo or configured_repo()
    if repo and not args.no_sync:
        return sync_repo(args.out, repo, args.force, started)
    return 0


if __name__ == "__main__":
    sys.exit(main())
