#!/usr/bin/env python3
"""python3 scripts/prep_test.py — pin the two prep.py decisions that decide
whether a demo is alive, both of which fail SILENTLY behind a 200.

  framework()  names what the artifact is, and that drives COOP/COEP in
               clients/projects/sites.go. Call a Unity export "static" and its
               multithreaded wasm hangs forever on a page that answers 200.
  roots()      picks the directory to serve. Pick a framework's SOURCE
               index.html over its build output and every visitor gets a blank
               page — the failure that shipped an un-built create-react-app
               shell, and the one Flutter's web/ scaffold would repeat.
"""
import importlib.util, os, pathlib, sys, tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("prep", os.path.join(HERE, "prep.py"))
prep = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prep)

fails = []


def is_(name, got, want):
    if got != want:
        fails.append(name)
    print("%s %-38s %s" % ("ok  " if got == want else "FAIL", name, got))


def tree(files):
    d = tempfile.mkdtemp()
    for f, c in files.items():
        p = pathlib.Path(d, f)
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(c)
    return d


# framework(): the closed set crossOriginIsolated() understands, and nothing else
is_("unity export", prep.framework(tree({
    "index.html": "", "Build/x.loader.js": "", "Build/x.wasm": "", "Build/x.data": ""})), "unity")
is_("godot export", prep.framework(tree({
    "index.html": "", "orb.pck": "", "orb.wasm": "", "orb.js": ""})), "godot")
is_("unreal export", prep.framework(tree({
    "index.html": "", "g.utoc": "", "g.wasm": ""})), "unreal")
is_("plain site", prep.framework(tree({"index.html": "", "app.js": ""})), "static")
# A page may ship wasm without being a game (sqlite, ffmpeg): isolating it would
# break the third-party embeds it is allowed to make, so the marker is required.
is_("wasm that is not a game", prep.framework(tree({
    "index.html": "", "sqlite.wasm": "", "app.js": ""})), "static")
is_("pck without wasm", prep.framework(tree({"index.html": "", "data.pck": ""})), "static")

# roots(): a page whose own same-origin scripts are not on disk cannot run
FLUTTER = ('<!DOCTYPE html><html><head><base href="/"></head><body>'
           '<script src="flutter_bootstrap.js" async></script></body></html>')
d = tree({"web/index.html": FLUTTER, "web/manifest.json": "{}",
          "build/web/index.html": FLUTTER, "build/web/flutter_bootstrap.js": "//",
          "build/web/main.dart.js": "//"})
is_("flutter build beats its scaffold", os.path.relpath(prep.roots(d)[0], d), "build/web")
d = tree({"index.html": '<script src="/app.js"></script>', "app.js": "//"})
is_("built site is a root", [os.path.relpath(x, d) for x in prep.roots(d)], ["."])
d = tree({"index.html": '<script src="https://a.hanzo.ai/analytics.js"></script>'})
is_("CDN script is not ours to resolve", [os.path.relpath(x, d) for x in prep.roots(d)], ["."])
d = tree({"index.html": "<h1>hi</h1>"})
is_("script-free page is a root", [os.path.relpath(x, d) for x in prep.roots(d)], ["."])
is_("missing script disqualifies", prep.roots(tree({"index.html": '<script src="main.js"></script>'})), [])

# The Flutter lane needs an x64 SDK (Google ships no Linux arm64 build). Where
# there is none it must skip, not half-build: roots() then reports NO-SITE.
d = tree({"pubspec.yaml": "name: x\n"})
os.environ["PATH"] = ""
prep.build(d)
is_("flutter lane skips without an SDK", os.path.isdir(os.path.join(d, "build")), False)

print("\n%s (%d checks)" % ("FAILED: " + ", ".join(fails) if fails else "ALL PASS", 12))
sys.exit(1 if fails else 0)
