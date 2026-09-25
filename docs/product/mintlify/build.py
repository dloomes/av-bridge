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
  python docs/product/mintlify/build.py --out C:/path/to/mintlify-repo
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
SRC_DIR = HERE.parent

# ---------------------------------------------------------------------------
# Content map
# ---------------------------------------------------------------------------

# Single-page documents. Add the rest of the doc pack here as each is ready.
PAGES: dict[str, dict] = {}

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
                    description="Host specifications per Collector and when to add another."),
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
                     description="Update each runtime and the version compatibility commitment."),
            12: dict(slug="monitoring", title="Monitor health and logs",
                     sidebarTitle="Health & logs", nav="Operate", icon="heart-pulse",
                     description="Local health checks, Prometheus metrics, portal status and logs."),
            13: dict(slug="backup", title="Back up and restore", sidebarTitle="Backup & restore",
                     nav="Operate", icon="box-archive",
                     description="What the Collector stores locally and what, if anything, to "
                                 "back up."),
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
    for key in ("title", "sidebarTitle", "description", "icon"):
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


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("docs", nargs="*", help="documents to build (default: all)")
    ap.add_argument("--out", type=Path, default=HERE / "out")
    args = ap.parse_args()

    # "devices" is the Supported devices section, generated from the adapter
    # catalogue by adapter_docs.py rather than from a Markdown source.
    known = {**PAGES, **GUIDES, "devices": None}
    names = args.docs or list(known)
    unknown = [n for n in names if n not in known]
    if unknown:
        print(f"unknown document(s): {', '.join(unknown)}; known: {', '.join(known)}",
              file=sys.stderr)
        return 1

    nav = []
    for n in names:
        if n == "devices":
            import adapter_docs
            files, group = adapter_docs.build(args.out, convert, frontmatter, yaml_str)
            nav.append(group)
        elif n in GUIDES:
            files = build_guide(n, GUIDES[n], args.out)
            nav.append(guide_nav(GUIDES[n]))
        else:
            files = build_page(n, PAGES[n], args.out)
            nav.append({"group": PAGES[n].get("group", "Documentation"), "pages": [n]})
        for f in files:
            print(f"wrote {f}")

    nav_file = args.out / "docs-navigation.json"
    nav_file.write_text(json.dumps(nav, indent=2) + "\n", encoding="utf-8")
    print(f"wrote {nav_file}  (paste into docs.json -> navigation)")

    # Hand-written Mintlify-only pages (no Word equivalent), e.g. quickstart.
    for src in sorted((HERE / "static").glob("*.mdx")):
        dest = args.out / src.name
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
    return 0


if __name__ == "__main__":
    sys.exit(main())
