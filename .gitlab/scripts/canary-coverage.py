#!/usr/bin/env python3
"""Fail when canary-full and the modules requiring the core disagree.

The matrix is hand-written and a new sibling does not add itself; seven went
uncovered that way (go/config#9).
"""

import json
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

API = "https://gitlab.com/api/v4"
GROUP = "phpboyscout/go"
CORE = "gitlab.com/phpboyscout/go/config"
CI_FILE = ".gitlab-ci.yml"


def get(url):
    with urllib.request.urlopen(url, timeout=30) as resp:
        return resp.read().decode()


def group_projects():
    group = urllib.parse.quote(GROUP, safe="")
    page = 1
    while True:
        batch = json.loads(get(f"{API}/groups/{group}/projects?per_page=100&archived=false&page={page}"))
        if not batch:
            return
        yield from batch
        page += 1


def requires_core(project):
    path = urllib.parse.quote(project["path_with_namespace"], safe="")
    branch = urllib.parse.quote(project.get("default_branch") or "main", safe="")
    try:
        gomod = get(f"{API}/projects/{path}/repository/files/go.mod/raw?ref={branch}")
    except urllib.error.HTTPError as err:
        if err.code == 404:
            return False
        raise
    return re.search(rf"^\s*(require\s+)?{re.escape(CORE)}\s+v", gomod, re.M) is not None


def canary_full_matrix():
    text = open(CI_FILE).read()
    block = re.search(r"^canary-full:\n(.*?)^\s+rules:", text, re.S | re.M)
    if block is None:
        sys.exit(f"canary-coverage: no canary-full job in {CI_FILE}")
    return set(re.findall(r"^\s+- (config-[a-z0-9-]+)\s*$", block.group(1), re.M))


def main():
    family = {p["path"] for p in group_projects() if p["path"] != "config" and requires_core(p)}
    covered = canary_full_matrix()

    missing = sorted(family - covered)
    stale = sorted(covered - family)

    print(f"canary-coverage: {len(family)} modules require the core; canary-full covers {len(covered)}")

    for name in missing:
        print(f"  MISSING from canary-full: {name}")
    for name in stale:
        print(f"  in canary-full but no longer requires the core: {name}")

    if missing or stale:
        sys.exit(1)


if __name__ == "__main__":
    main()
