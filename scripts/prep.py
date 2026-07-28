#!/usr/bin/env python3
"""prep.py <repo-dir> <out.tgz> — turn one hanzo-templates checkout into a
servable static artifact with the Hanzo runtime injected.

ONE artifact shape (tar.gz of the built site, index.html at the root) so the
whole batch rides the SAME deploy route as the console one-click upload
(POST /v1/projects/:slug/deploy). Binary assets survive because the artifact is
a tarball, not a JSON string manifest.
"""
import json, os, re, shutil, subprocess, sys, tarfile, zipfile

# The Hanzo runtime: ONE shared snippet served from the existing widget CDN
# (hanzoai/a). Injecting the tags instead of vendoring the scripts means the
# runtime is updated once, at a.hanzo.ai, never across 71 template repos.
SNIPPET = (
    '<script src="https://a.hanzo.ai/analytics.js" data-org="hanzo" defer></script>\n'
    '<script src="https://a.hanzo.ai/chat.js" data-org="hanzo" data-mode="site" defer></script>\n'
)

# `previews/` is a template repo's VENDORED copy of OTHER templates' builds
# (hanzo-templates/gallery ships two). It holds an index.html, so root detection
# picked it whenever the repo's own build had not landed yet — that is why the
# gallery demo served a byte-identical copy of the beta demo. Skipping it drops
# the wrong root and the dead weight in one move.
SKIP_DIRS = {".git", "node_modules", ".next", ".cache", "__MACOSX", "previews"}
# Build output dirs, best first: a built site beats the sources it came from.
PREF = ["out", "dist", "build", "_site", "public", "html", "site", "www", "."]


def walk(d):
    for dp, dns, fs in os.walk(d):
        dns[:] = [x for x in dns if x not in SKIP_DIRS]
        yield dp, fs


def roots(d):
    """Every directory holding an index.html, ranked: build-output name first,
    then shallowest, then most files — the layout a template ships its demo in."""
    out = []
    for dp, fs in walk(d):
        if "index.html" not in fs:
            continue
        # A create-react-app `public/index.html` is the SOURCE template: it still
        # holds %PUBLIC_URL% placeholders and no bundle tag, so serving it gives a
        # blank page that answers 200 — worse than an honest failure. It is a root
        # only once a build has rewritten it.
        # BOTH halves are required. Checking %PUBLIC_URL% alone rejects BUILT pages
        # that merely mention it inside og:image/twitter:image meta — prism ships
        # five real /_next/static/chunks/*.js tags and would be thrown away.
        # The bundle must be a SAME-ORIGIN one. Any absolute-URL script counts a
        # third party's file as proof the page was built, and the third party
        # this hits is us: inject() writes an https://a.hanzo.ai/analytics.js tag
        # into every html it touches, so one earlier run made cipher-react's and
        # construct's untouched CRA source template permanently pass the test.
        try:
            html = open(os.path.join(dp, "index.html"),
                        encoding="utf-8", errors="ignore").read()
            if "%PUBLIC_URL%" in html and not re.search(
                    r"<script[^>]+src=[\"'](?!\w+:|//)[^\"']*\.js", html, re.I):
                continue
        except OSError:
            continue
        rel = os.path.relpath(dp, d)
        base = os.path.basename(dp) if rel != "." else "."
        rank = PREF.index(base) if base in PREF else len(PREF)
        out.append((rank, rel.count(os.sep), -len(fs), dp))
    out.sort()
    return [x[3] for x in out]


def unzip(d):
    """Templates that ship their demo as a zip (restoq, blocks/react.zip) are
    unpacked in place so root detection sees the same shape as every other one."""
    for dp, fs in walk(d):
        for f in fs:
            if not f.lower().endswith(".zip"):
                continue
            p = os.path.join(dp, f)
            try:
                if os.path.getsize(p) > 200 << 20:
                    continue
                with zipfile.ZipFile(p) as z:
                    if not any(n.endswith("index.html") for n in z.namelist()):
                        continue
                    z.extractall(os.path.join(dp, f[:-4] + ".unzipped"))
            except Exception:
                pass


# Next.js static export. The config's default export is wrapped (never replaced)
# so plugin-built configs — fumadocs' createMDX, next-mdx — keep working; only
# the export/image knobs are forced.
# Lint and typecheck are deliberately NOT suppressed. Forcing
# eslint.ignoreDuringBuilds + typescript.ignoreBuildErrors made every template
# "build", which is not the same as every template WORKING: a type error that
# breaks a page at runtime shipped as a green build. A template that cannot
# typecheck is a broken template and has to be fixed or reported, not hidden.
FORCE = "{output:'export',images:{unoptimized:true},trailingSlash:true}"
# A plain merge, no wrapper lambda: a rest parameter is an implicit `any`, so
# the old wrapper was itself the first thing a typechecked next.config.ts
# refused to compile — the suppression above was hiding prep.py's own bug.
MERGE = "Object.assign({}, __hanzoStatic, %s)" % FORCE


def patch_next(cfg):
    src = open(cfg, encoding="utf-8", errors="ignore").read()
    if "__hanzoStatic" in src:
        return
    if re.search(r"^\s*export\s+default\s", src, re.M):
        src = re.sub(r"^(\s*)export\s+default\s", r"\1const __hanzoStatic = ", src, count=1, flags=re.M)
        src += "\nexport default %s;\n" % MERGE
    else:
        src += "\nconst __hanzoStatic = module.exports; module.exports = %s;\n" % MERGE
    open(cfg, "w", encoding="utf-8").write(src)


def run(cmd, cwd, timeout):
    try:
        r = subprocess.run(cmd, cwd=cwd, shell=True, timeout=timeout, capture_output=True,
                           env={**os.environ, "CI": "1", "NEXT_TELEMETRY_DISABLED": "1",
                                "npm_config_audit": "false", "npm_config_fund": "false"})
        return r.returncode == 0, (r.stdout + r.stderr).decode("utf-8", "replace")
    except Exception as e:
        return False, str(e)


# Several templates were scaffolded against shadcn/ui but never vendored the
# primitives they import, so `next build` dies on "Can't resolve
# '@/components/ui/textarea'". The components are public registry values, so the
# missing ones are FETCHED rather than hand-written — one heal for every repo
# with the same hole, and none of them is a template edit we have to maintain.
MISSING_UI = re.compile(r"Can't resolve '@/components/ui/([\w-]+)'")
# A dependency the template imports but never declared, and a version pin npm
# cannot satisfy (@next/font@15.3.5 does not exist) — two more ways a template
# repo is simply incomplete, each with one mechanical repair.
MISSING_DEP = re.compile(r"Can't resolve '(@[\w.-]+/[\w.-]+|[a-z][\w.-]*)'")
BAD_PIN = re.compile(r"No matching version found for (@?[\w.\-/]+)@")
# The repo pinned a typescript older than its own tsconfig asks for
# ("moduleResolution": "bundler" needs >= 5.0), so tsc rejects the project
# before a single page is built — nextjs-template's whole failure.
STALE_TS = re.compile(r"error TS(?:5023|5095|6046)\b")
# npm resolves optionalDependencies from the LOCKFILE, so a package-lock.json
# committed on x64 never names this host's native binary and npm installs
# nothing for it (npm/cli#4828). lightningcss and @tailwindcss/oxide both then
# die at require time — the failure that made most of these templates NOROOT.
# The package that needs its sibling is the first frame of the require stack;
# the sibling's name is that package plus THIS host's triple, so the repair is
# arch-derived rather than hardcoded to whatever machine ran the build.
NATIVE_MISS = re.compile(r"Cannot find module '[^']*?[/.]([\w-]+)\.linux-[\w-]+\.node'")
# The SAME miss, reported the other way. A napi-rs loader swallows the resolve
# error into `new Error(..., {cause})`, and next build prints only the outer
# message — so the text above never appears and the repair silently never fired
# (blog: @tailwindcss/oxide, NOROOT). Here the package is the frame that threw.
NATIVE_THROW = re.compile(
    r"Failed to load native binding[\s\S]{0,240}?node_modules/((?:@[\w.-]+/)?[\w.-]+)/index\.js")
TRIPLE = "linux-%s-gnu" % {"aarch64": "arm64", "x86_64": "x64"}.get(os.uname().machine,
                                                                   os.uname().machine)


def heal_native(d, out):
    pkgs = {"%s-%s" % (p, TRIPLE) for p in NATIVE_THROW.findall(out)}
    for base in NATIVE_MISS.findall(out):
        # Scoped packages (@tailwindcss/oxide) need the scope back: the require
        # stack carries the full path, the module name in the error does not.
        m = re.search(r"node_modules/((?:@[\w.-]+/)?%s)\b" % re.escape(base), out)
        pkgs.add("%s-%s" % (m.group(1) if m else base, TRIPLE))
    if pkgs:
        run("npm install --no-audit --no-fund " + " ".join(sorted(pkgs)), d, 900)
    return bool(pkgs)


def heal_deps(d, out):
    if STALE_TS.search(out):
        run("npm install --legacy-peer-deps --no-audit --no-fund typescript@latest", d, 900)
        return True
    pins = set(BAD_PIN.findall(out))
    if pins:
        pj = os.path.join(d, "package.json")
        j = json.load(open(pj))
        for k in ("dependencies", "devDependencies"):
            for p in pins & set(j.get(k) or {}):
                j[k][p] = "latest"
        json.dump(j, open(pj, "w"), indent=2)
        run("npm install --legacy-peer-deps --no-audit --no-fund", d, 1800)
        return True
    miss = {m for m in MISSING_DEP.findall(out) if not m.startswith(".")}
    if miss:
        run("npm install --legacy-peer-deps --no-audit --no-fund " + " ".join(sorted(miss)), d, 1200)
    return bool(miss)


# `output: export` renders everything at BUILD time, and two kinds of app-dir
# file argue with that. Each has a mechanical repair that KEEPS the file.
META_IMG = ("opengraph-image", "twitter-image", "icon.", "apple-icon")
CODE = (".ts", ".tsx", ".js", ".jsx", ".mjs")
EDGE = re.compile(r"^[ \t]*export[ \t]+const[ \t]+runtime[ \t]*=.*$\n?", re.M)


def force_static(p):
    """Pin one app-dir route to build-time rendering — drop the edge pin (the
    edge runtime has no build-time render) and force-static, which makes
    headers()/cookies() return empty values instead of bailing the export out.
    Inside a dynamic segment that is not enough: the export also needs the
    segment's params, and the sibling page already computes them, so they are
    re-exported. True when the file changed, False when nothing was left to add."""
    s = open(p, encoding="utf-8", errors="ignore").read()
    if re.match(r"\s*[\"']use client[\"']", s):
        return False  # a client component may export neither of these — writing
        # them there trades one build error for a worse one
    add = "" if "force-static" in s else "\nexport const dynamic = 'force-static';\n"
    dp = os.path.dirname(p)
    if "generateStaticParams" not in s and os.path.basename(dp).startswith("["):
        # The page of the segment is the one file with nothing to inherit from.
        # An un-enumerable segment exports as zero pages, which ships the rest
        # of the site — the alternative on offer is no site at all.
        add += ("export { generateStaticParams } from './page';\n"
                if not p.startswith(os.path.join(dp, "page."))
                and any(os.path.isfile(os.path.join(dp, "page" + e)) for e in CODE)
                else "export async function generateStaticParams(){return []}\n")
    if not add:
        return False
    open(p, "w", encoding="utf-8").write(EDGE.sub("", s) + add)
    return True


def heal_export(d, out):
    """A generated metadata image — opengraph, twitter, icon, apple-icon — DOES
    static-export once it is pinned to build time, so it is pinned, never
    deleted: deleting it (what this did before) shipped every demo with no
    social card and no favicon, and said nothing. A route handler is only
    exportable as a force-static GET, so it is pinned first and dropped only if
    the next build still refuses it — a static site cannot answer a POST.
    `is missing "generateStaticParams()"` is the THIRD way the export refuses a
    file and was missing from this gate, so the loop exited on the first repair
    it made and blog/gallery2/quantum all came out NOROOT: the fix had been
    applied and was never given the retry that would have proved it."""
    if not any(k in out for k in ("Failed to collect page data", "dynamic = ",
                                  'is missing "generateStaticParams()"',
                                  "couldn't be rendered statically")):
        return False
    hit = False
    for dp, dns, fs in os.walk(d):
        dns[:] = [x for x in dns if x not in SKIP_DIRS]
        if os.sep + "app" + os.sep not in dp + os.sep:
            continue
        dyn = os.path.basename(dp).startswith("[")
        for f in fs:
            if not f.endswith(CODE):
                continue  # icon.png and friends are already static assets
            p = os.path.join(dp, f)
            # A layout is pinned for the same reason a metadata image is: it is
            # the one file every route inherits, so deploy's headers() call in
            # generateViewport made even /_not-found un-exportable.
            if f.startswith(META_IMG) or f.startswith("layout.") or (dyn and f.startswith("page.")):
                hit |= force_static(p)
            elif f.startswith("route."):
                force_static(p) or os.remove(p)
                hit = True
    return hit


def heal_ui(d, out):
    import urllib.request
    names = set(MISSING_UI.findall(out))
    if not names:
        return False
    uidir = next((os.path.join(dp, "ui") for dp, _ in walk(d)
                  if os.path.basename(dp) == "components" and os.path.isdir(os.path.join(dp, "ui"))),
                 os.path.join(d, "components", "ui"))
    os.makedirs(uidir, exist_ok=True)
    deps = set()
    for n in names:
        try:
            with urllib.request.urlopen(
                    "https://ui.shadcn.com/r/styles/default/%s.json" % n, timeout=30) as r:
                item = json.load(r)
        except Exception:
            continue
        for f in item.get("files") or []:
            open(os.path.join(uidir, os.path.basename(f["path"])), "w").write(f["content"])
        deps |= set(item.get("dependencies") or [])
    if deps:
        run("npm install --legacy-peer-deps --no-audit --no-fund " + " ".join(sorted(deps)), d, 900)
    return True


def buildable(d):
    """Project dirs worth building: the repo root, then any single-level child
    that is its own project (launch/'Teaser - SaaS Landing Page', soar/garuda)."""
    yield d
    for n in sorted(os.listdir(d)):
        p = os.path.join(d, n)
        if n not in SKIP_DIRS and os.path.isdir(p) and os.path.isfile(os.path.join(p, "package.json")):
            yield p


def build(d):
    pj = os.path.join(d, "package.json")
    if not os.path.isfile(pj):
        return
    try:
        j = json.load(open(pj))
    except Exception:
        return
    deps = {**(j.get("dependencies") or {}), **(j.get("devDependencies") or {})}
    scripts = j.get("scripts") or {}
    if "build" not in scripts:
        return
    if "next" in deps:
        # `out/` is OURS — it exists only because FORCE asks for it — so the
        # last run's copy is cleared. Left in place it makes a build that now
        # FAILS still answer OK: roots() finds the stale export and packs it.
        shutil.rmtree(os.path.join(d, "out"), ignore_errors=True)
        for c in ("next.config.js", "next.config.mjs", "next.config.ts"):
            if os.path.isfile(os.path.join(d, c)):
                patch_next(os.path.join(d, c))
                break
        else:
            open(os.path.join(d, "next.config.js"), "w").write(
                "const __hanzoStatic = {}; module.exports = %s;\n" % MERGE)
    elif not ({"vite", "react-scripts", "gulp", "parcel", "astro", "@11ty/eleventy"} & set(deps)):
        return  # not a static web build (design kit, font pack, monorepo shell)
    ok, out = run("npm install --legacy-peer-deps --no-audit --no-fund", d, 1800)
    if not ok:
        # `workspace:*` is a protocol npm cannot parse at all (EUNSUPPORTEDPROTOCOL),
        # and papers ships those deps with NO pnpm-lock.yaml — so gating this
        # fallback on the lockfile left it with no node_modules at all and a
        # build whose only output was `sh: 1: next: not found`.
        ok, out = run("pnpm install --no-frozen-lockfile", d, 1800)
    if not ok:
        heal_deps(d, out)
    # Build, repairing what the failure names, until it builds or nothing is
    # left to repair. Templates are incomplete in a handful of mechanical ways;
    # each heal is one of them, and the loop is the ONE place they compose.
    for _ in range(6):
        ok, out = run("npm run build", d, 1800)
        if ok or not (heal_native(d, out) or heal_ui(d, out) or heal_deps(d, out)
                      or heal_export(d, out)):
            break
    # Next < 13.3 has no `output: 'export'`; the static site is a second command.
    if "next" in deps and not os.path.isfile(os.path.join(d, "out", "index.html")):
        run("npx --no-install next export", d, 900)


# A multi-page template ships a page-LIST scaffold at index.html: a bare column
# of links to its sibling pages ("Page List", "Index Page"). That is the format
# VARIANT of the template, not a page of it — serving it as the demo shows a
# directory instead of the design, and it is why four live demos rendered a link
# dump. A scaffold is recognisable without trusting its title: many sibling
# .html links and almost no prose of its own (measured: 22-27 chars per link,
# versus 234+ for the thinnest real page).
LANDING = ("home", "index", "main", "landing", "demo", "overview", "dashboard", "start")


def pick_index(root):
    """Point index.html at the template's best real page when it is a page-list
    scaffold, keeping the scaffold at pages.html so no page is lost."""
    p = os.path.join(root, "index.html")
    try:
        h = open(p, encoding="utf-8", errors="ignore").read()
    except OSError:
        return None
    # A scaffold links its pages in either of two shapes and prism proved the
    # second one has to count: a plain export writes sibling `about.html`, a
    # trailingSlash Next export writes the route folder `about/`, and matching
    # only the first left prism's purple link dump live.
    links = set()
    for x in re.findall(r'href="([^"#?]+)"', h):
        if re.match(r"\w+:|//", x):
            continue
        x = x.strip("/") + ("" if x.endswith((".html", ".htm")) else "/index.html")
        if x != "index.html" and os.path.isfile(os.path.join(root, x)):
            links.add(x)
    body = re.sub(r"<[^>]+>", " ", re.sub(r"(?is)<(script|style).*?</\1>", " ", h))
    if len(links) < 6 or len(" ".join(body.split())) >= 60 * len(links):
        return None
    # The landing page by name — matched on the filename's hyphen tokens, so
    # `circle-overview.html` counts as an overview — else the heaviest page,
    # since the one with the most markup is the fullest demo of the design.
    def rank(x):
        # The LEADING token names the page; a later one only qualifies it. Ranking
        # on any token let prism's `profile/index-v2` outscore `dashboard/social`
        # because "index" is a landing word wherever it appears.
        head = re.split(r"[-/]", x[:-11] if x.endswith("/index.html") else x.rsplit(".", 1)[0])[0]
        return (LANDING.index(head) if head in LANDING else len(LANDING),
                -os.path.getsize(os.path.join(root, x)), x)

    best = min(links, key=rank)
    os.replace(p, os.path.join(root, "pages.html"))
    # A redirect, not a copy of the page's markup: copying is only right while
    # the page is a SIBLING, and a route folder's page resolves its assets and
    # its client router from its own path, so the copy would land broken.
    # A route folder is entered by its folder URL: its client router matches on
    # the path it was exported at, not on the index.html underneath it.
    dest = best[:-10] if best.endswith("/index.html") else best
    open(p, "w", encoding="utf-8").write(
        '<!doctype html><meta http-equiv="refresh" content="0;url=%s">'
        '<title>Demo</title><script>location.replace(%s)</script>' % (dest, json.dumps(dest)))
    return best


def inject(root):
    n = 0
    for dp, fs in walk(root):
        for f in fs:
            if not f.endswith((".html", ".htm")):
                continue
            p = os.path.join(dp, f)
            try:
                t = open(p, encoding="utf-8", errors="ignore").read()
            except Exception:
                continue
            if "a.hanzo.ai/analytics.js" in t:
                continue
            if "</head>" in t:
                t = t.replace("</head>", SNIPPET + "</head>", 1)
            elif "</body>" in t:
                t = t.replace("</body>", SNIPPET + "</body>", 1)
            else:
                t += SNIPPET
            try:
                open(p, "w", encoding="utf-8").write(t)
                n += 1
            except Exception:
                pass
    return n


def pack(root, out):
    n = size = 0
    with tarfile.open(out, "w:gz") as tf:
        for dp, fs in walk(root):
            for f in fs:
                p = os.path.join(dp, f)
                # Source maps are a debugger's copy of code the demo already
                # ships; nothing renders from them and they are routinely the
                # single heaviest file in a build (kinetic: 1.5 MB of 4.5 MB).
                if os.path.islink(p) or f.endswith(".map"):
                    continue
                try:
                    size += os.path.getsize(p)
                except OSError:
                    continue
                tf.add(p, arcname=os.path.relpath(p, root))
                n += 1
    return n, size


# The live edge caps a request body at 4 MiB (fiber's default; cloud's
# GATEWAY_BODY_LIMIT is not taking effect in v1.801.266 — see LLM.md). A demo
# must therefore FIT, and a 40 MB template is a bad demo anyway: its weight is
# unoptimized hero imagery. fit() re-encodes the heavy media IN PLACE — same
# path, same container format, so every href/src in the template still resolves
# — and never deletes a file, so nothing renders broken.
PASSES = [(1920, 82), (1600, 74), (1280, 66), (1024, 58), (800, 50), (640, 44), (512, 40)]


def shrink_images(root, maxdim, q):
    from PIL import Image, ImageSequence
    for dp, fs in walk(root):
        for f in fs:
            if not f.lower().endswith((".jpg", ".jpeg", ".png", ".webp", ".gif")):
                continue
            p = os.path.join(dp, f)
            try:
                if os.path.getsize(p) < 20 << 10:
                    continue
                im = Image.open(p)
                fmt = im.format
                scale = min(1.0, maxdim / max(im.size))
                size = (max(1, int(im.width * scale)), max(1, int(im.height * scale)))
                if fmt == "GIF" and getattr(im, "n_frames", 1) > 1:
                    # An animated GIF's weight is frames × pixels, so resizing
                    # alone barely moves a 7 MB loop. Later passes also drop
                    # frames (and stretch each one's duration to match), which
                    # keeps the animation playing at the same speed.
                    step = max(1, 6 - maxdim // 320)
                    frames = [fr.copy().convert("RGB").resize(size)
                              for i, fr in enumerate(ImageSequence.Iterator(im)) if i % step == 0]
                    frames[0].save(p, "GIF", save_all=True, append_images=frames[1:],
                                   optimize=True, loop=0,
                                   duration=im.info.get("duration", 80) * step)
                    continue
                im.load()
                if max(im.size) > maxdim:
                    im.thumbnail((maxdim, maxdim), Image.LANCZOS)
                if fmt == "JPEG":
                    im.convert("RGB").save(p, "JPEG", quality=q, optimize=True, progressive=True)
                elif fmt == "WEBP":
                    im.save(p, "WEBP", quality=q, method=4)
                else:  # PNG / GIF still-frame: palette-quantize, which is where the bytes are
                    a = im.convert("RGBA")
                    im = a.quantize(colors=256, method=Image.FASTOCTREE)
                    im.save(p, fmt, optimize=True)
            except Exception:
                pass


def shrink_videos(root, w, crf):
    for dp, fs in walk(root):
        for f in fs:
            if not f.lower().endswith((".mp4", ".webm", ".mov", ".m4v")):
                continue
            p = os.path.join(dp, f)
            try:
                if os.path.getsize(p) < 512 << 10:
                    continue
                tmp = p + ".sh.mp4"
                if run("ffmpeg -y -loglevel error -i %r -vf scale=%d:-2 -c:v libx264 -crf %d "
                       "-preset veryfast -movflags +faststart -an %r" % (p, w, crf, tmp), dp, 900)[0] \
                        and os.path.getsize(tmp) < os.path.getsize(p):
                    os.replace(tmp, p)
                else:
                    os.path.exists(tmp) and os.remove(tmp)
            except Exception:
                pass


def fit(root, out, cap):
    files, size = pack(root, out)
    for i, (maxdim, q) in enumerate(PASSES):
        if os.path.getsize(out) <= cap:
            break
        shrink_images(root, maxdim, q)
        if i == 0:
            shrink_videos(root, 1280, 30)
        files, size = pack(root, out)
    return files, size


def main():
    d, out = sys.argv[1], sys.argv[2]
    cap = int(sys.argv[3]) if len(sys.argv) > 3 else 0
    unzip(d)
    # Build BEFORE looking for a root, always. A framework repo ships an
    # index.html that is a SOURCE entry (vite) or an unrelated preview
    # (gallery/public/previews) — serving either is a broken demo — and build()
    # is a no-op for a repo carrying no web-build framework, so this is one path
    # rather than a "prebuilt or built" fork that picks the wrong one.
    for p in buildable(d):
        build(p)
    r = roots(d)
    if not r:
        print("NOROOT")
        return 2
    root = r[0]
    landed = pick_index(root)
    inject(root)
    files, size = fit(root, out, cap) if cap else pack(root, out)
    print("OK %s %d %d%s" % (os.path.relpath(root, d), files, size,
                             " index=" + landed if landed else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
