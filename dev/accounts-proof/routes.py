#!/usr/bin/env python3
"""The pure parts of the accounts proof (run.sh calls them as `python -I routes.py <command>`).

  plan ROUTES TABLE_GO UI_CALLS     METHOD<TAB>PATH<TAB>denied|open for every route a user may or may not call
  uicalls UI_CALLS APP              METHOD<TAB>PATH<TAB>kind of every dashboard call, with the fixture app's name
  approutes ROUTES APP              METHOD<TAB>PATH of every route that names one app of AppManagement
  compare-ui UI_COPY GATEWAY_FILE   exit 0 when the UI's copy of ui-calls.txt is the Gateway's file
  same STATUS_A FILE_A NAME_A STATUS_B FILE_B NAME_B   exit 0 when two answers match, apart from the app's name
  jget PATH / jfind PATH KEY VALUE OUT   read a JSON document on stdin
  jobj KEY VALUE [KEY VALUE...]     a JSON object of strings, each value escaped (a password may hold a quote)
  regen                             rebuild routes.txt with the Gateway's gen-routes.py (needs PyYAML and git)

ROUTES is route/testdata/routes.txt of the Gateway; TABLE_GO its route/allowtable.go.
"""
import fnmatch
import json
import os
import re
import subprocess
import sys

NAMED_APP = ("compose", "container", "stats", "git")
APP_PARAMS = ("{id}", "{app}")


def parse_routes(text):
    """The (method, pattern) pairs of routes.txt; ANY becomes GET and POST."""
    out = []
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        method, pattern = line.split(None, 1)
        for m in (("GET", "POST") if method == "ANY" else (method,)):
            out.append((m, pattern.strip()))
    return out


def is_param(seg):
    return seg.startswith(":") or seg.startswith("*") or (seg.startswith("{") and seg.endswith("}"))


def concretize(pattern, fill="x"):
    """A path a request may carry for a route pattern: every parameter written `fill`."""
    return "/".join(fill if is_param(s) else s for s in pattern.split("/"))


def parse_table_go(text):
    """The public and base rows of the Gateway's allowTable (module rows open only with a right)."""
    return re.findall(r'\{"([A-Z]+)",\s*"([^"]+)",\s*(public|base)\}', text)


OPEN_KINDS = ("public", "base")


def open_rows(rows):
    """The rows a user with no right may call: a `module:<right>` row opens only with that right."""
    return [r for r in rows if r[2] in OPEN_KINDS]


def parse_ui_calls(text):
    rows = []
    for line in text.splitlines():
        f = line.split()
        if len(f) == 3 and not line.lstrip().startswith("#"):
            rows.append((f[0], f[1], f[2]))
    return rows


def allowed(method, path, rows):
    """Whether a user with no right may make the call: a row's `*` is one path segment, a GET row opens HEAD."""
    segs = path.rstrip("/").split("/")
    for rmethod, pattern, _ in rows:
        if rmethod != method and not (rmethod == "GET" and method == "HEAD"):
            continue
        psegs = pattern.rstrip("/").split("/")
        if len(psegs) == len(segs) and all(fnmatch.fnmatchcase(s, p) for s, p in zip(segs, psegs)):
            return True
    return False


# the Gateway answers it itself, to any token, before the table (readonly_test.go checks it with the token of a gone account)
GATEWAY_OWN = [("GET", "/ping", "public")]


def plan(routes, rows):
    rows = open_rows(rows) + GATEWAY_OWN
    seen, out = set(), []
    for method, pattern in routes:
        path = concretize(pattern)
        item = (method, path, "open" if allowed(method, path, rows) else "denied")
        if item not in seen:
            seen.add(item)
            out.append(item)
    return out


def ui_calls(text, app):
    # the calls were read with the app of the fixtures of the Gateway's tests, immich
    return [(m, p.replace("immich", app), k) for m, p, k in parse_ui_calls(text)]


def app_routes(routes, app):
    out = []
    for method, pattern in routes:
        segs = pattern.split("/")
        if len(segs) < 4 or segs[1:3] != ["v2", "app_management"] or segs[3] not in NAMED_APP:
            continue
        if not any(s in APP_PARAMS for s in segs):
            continue
        out.append((method, "/".join(app if s in APP_PARAMS else ("x" if is_param(s) else s) for s in segs)))
    return out


def ui_copy_equal(ui_text, gateway_text):
    """The UI's copy is the Gateway's file under one `# COPY of` line."""
    ui = ui_text.replace("\r\n", "\n")
    head, _, rest = ui.partition("\n")
    return head.startswith("# COPY of") and rest == gateway_text.replace("\r\n", "\n")


def same_answer(a, name_a, b, name_b):
    """Two (status, body) answers that differ in nothing but the name the request carried."""
    return a[0] == b[0] and a[1].replace(name_a, "@APP@") == b[1].replace(name_b, "@APP@")


def jobj(pairs):
    return json.dumps(dict(zip(pairs[0::2], pairs[1::2])))


def jget(doc, path):
    try:
        cur = json.loads(doc)
        for key in path.split("."):
            cur = cur[int(key)] if isinstance(cur, list) else cur[key]
    except (ValueError, KeyError, IndexError, TypeError):
        return ""
    return "" if cur is None else str(cur)


def jfind(doc, list_path, key, value, out):
    try:
        rows = json.loads(doc)
        for k in list_path.split("."):
            rows = rows[k]
        return next((str(r[out]) for r in rows if str(r.get(key)) == value), "")
    except (ValueError, KeyError, TypeError, AttributeError):
        return ""


def read(path):
    with open(path, encoding="utf-8", newline="") as f:
        return f.read()


def tsv(items):
    for item in items:
        print("\t".join(item))


def regen():
    gw = os.environ["ACCOUNTS_PROOF_GATEWAY_REPO"]
    gen = os.path.join(gw, "route", "testdata", "gen-routes.py")
    args = []
    for var in ("CORE", "USERS", "AM"):
        args += [os.environ["ACCOUNTS_PROOF_%s_REPO" % var], os.environ.get("ACCOUNTS_PROOF_%s_REF" % var, "HEAD")]
    out = subprocess.run([sys.executable, gen] + args, check=True, capture_output=True, text=True, encoding="utf-8").stdout
    with open(os.path.join(gw, "route", "testdata", "routes.txt"), "w", encoding="utf-8", newline="\n") as f:
        f.write(out)


def main(argv):
    sys.stdout.reconfigure(newline="\n")  # the shell reads this: no CR on Windows
    cmd, a = argv[1], argv[2:]
    if cmd == "plan":
        tsv(plan(parse_routes(read(a[0])), parse_table_go(read(a[1])) + parse_ui_calls(read(a[2]))))
    elif cmd == "uicalls":
        tsv(ui_calls(read(a[0]), a[1]))
    elif cmd == "approutes":
        tsv(app_routes(parse_routes(read(a[0])), a[1]))
    elif cmd == "compare-ui":
        return 0 if ui_copy_equal(read(a[0]), read(a[1])) else 1
    elif cmd == "same":
        return 0 if same_answer((int(a[0]), read(a[1])), a[2], (int(a[3]), read(a[4])), a[5]) else 1
    elif cmd == "jget":
        print(jget(sys.stdin.read(), a[0]))
    elif cmd == "jobj":
        print(jobj(a))
    elif cmd == "jfind":
        print(jfind(sys.stdin.read(), a[0], a[1], a[2], a[3]))
    elif cmd == "regen":
        regen()
    else:
        print(__doc__, file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
