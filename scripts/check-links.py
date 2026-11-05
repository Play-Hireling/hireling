#!/usr/bin/env python3
"""Fail when a relative link in a markdown file points to a missing file or heading."""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
LINK = re.compile(r"(?<!!)\[[^\]]*\]\(([^)\s]+)\)")
SKIP_DIRS = {".git", "node_modules"}


def slug(heading: str) -> str:
    s = heading.strip().lower()
    s = re.sub(r"[`*_~]", "", s)
    s = re.sub(r"[^\w\- ]", "", s)
    return s.replace(" ", "-")


def anchors(path: pathlib.Path) -> set:
    out = set()
    text = path.read_text(encoding="utf-8")
    for line in text.splitlines():
        m = re.match(r"^#{1,6}\s+(.*)", line)
        if m:
            out.add(slug(m.group(1)))
    out.update(re.findall(r'<a id="([^"]+)"', text))
    return out


def main() -> int:
    bad = []
    for md in ROOT.rglob("*.md"):
        if any(p in SKIP_DIRS for p in md.parts):
            continue
        text = md.read_text(encoding="utf-8")
        text = re.sub(r"```.*?```", "", text, flags=re.S)  # ignore code blocks
        for target in LINK.findall(text):
            if re.match(r"^[a-z]+:", target):
                continue
            path, _, frag = target.partition("#")
            dest = md if path == "" else (md.parent / path).resolve()
            if not dest.exists():
                bad.append(f"{md.relative_to(ROOT)}: {target} (missing file)")
                continue
            if frag and dest.suffix == ".md" and frag not in anchors(dest):
                bad.append(f"{md.relative_to(ROOT)}: {target} (missing heading)")
    for b in bad:
        print(b)
    print(f"{len(bad)} broken link(s)")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
