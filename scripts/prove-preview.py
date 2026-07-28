#!/usr/bin/env python3
"""prove-preview.py <slug|url> ... — decide whether a demo RUNS, not whether it
answers 200.

Every cheaper check has already been wrong here. A status code passed an
un-built create-react-app shell. A DOM probe passes a Flutter app that never
booted (it paints into a shadow-root canvas, so `innerText` is empty either
way) and fails one that did. A bundle-size check passes a 404 screenshot.

The ONE signal that holds across every framework in the catalog — React, Expo
web, Flutter/CanvasKit, Unity WebGL, Godot, three.js — is the raster the user
actually sees. So: screenshot, interact, screenshot again.

    colours   distinct colours in the frame; a dead page is 1-3
    frames    requestAnimationFrame ticks in one second
    reacts    fraction of pixels that moved after the input

Exit status is the number of hosts that did not render, so the deploy batch
fails loudly instead of counting a blank page as shipped.

    python3 prove-preview.py unity-webgl godot-arcade
    python3 prove-preview.py --json out.json https://flutter.hanzo.app/
"""
import asyncio, json, os, sys

BLANK = 8          # a frame with fewer distinct colours than this rendered nothing
HOST = "https://%s.hanzo.app/"

TICK = """
async () => {
  let n = 0; const t0 = performance.now();
  await new Promise(r => { const s = () => { n++;
    if (performance.now() - t0 > 1000) return r(); requestAnimationFrame(s); };
    requestAnimationFrame(s); });
  return { frames: n, title: document.title, coi: self.crossOriginIsolated,
           sab: typeof SharedArrayBuffer !== 'undefined' };
}
"""


def frame(path):
    """Distinct colours, and the raw pixels, of one screenshot downsampled to a
    thumbnail — small enough that comparing two frames is cheap, large enough
    that a moving character still shows up."""
    from PIL import Image
    px = list(Image.open(path).convert("RGB").resize((160, 100)).getdata())
    return px, len(set(px))


def moved(a, b):
    n = sum(1 for x, y in zip(a, b)
            if abs(x[0] - y[0]) + abs(x[1] - y[1]) + abs(x[2] - y[2]) > 24)
    return round(n / len(a), 4)


async def prove(pw, url, shots):
    from playwright.async_api import Error as PWError
    # SwiftShader so WebGL/WebGPU content still rasterises on a headless CI box
    # with no GPU — without it every game scores as blank and the gate is a lie.
    br = await pw.chromium.launch(args=[
        "--enable-unsafe-swiftshader", "--use-gl=angle", "--use-angle=swiftshader",
        "--ignore-gpu-blocklist", "--no-sandbox"])
    pg = await (await br.new_context(viewport={"width": 1200, "height": 800})).new_page()
    slug = url.split("//")[-1].split(".")[0]
    r = {"url": url, "bad": [], "errors": []}
    pg.on("response", lambda x: r["bad"].append("%d %s" % (x.status, x.url[:100]))
          if x.status >= 400 else None)
    pg.on("pageerror", lambda e: r["errors"].append(str(e)[:160]))
    try:
        resp = await pg.goto(url, wait_until="domcontentloaded", timeout=90000)
        r["http"] = resp.status if resp else 0
        try:
            await pg.wait_for_load_state("networkidle", timeout=70000)
        except PWError:
            pass                       # a game that streams assets never idles
        await pg.wait_for_timeout(5000)
        r.update(await pg.evaluate(TICK))
        a = os.path.join(shots, slug + ".png")
        await pg.screenshot(path=a)
        pa, r["colors"] = frame(a)
        # One click and one arrow key: enough to move a game, open a route or
        # focus a field, and nothing a static page can fake.
        await pg.mouse.click(600, 400)
        await pg.keyboard.press("ArrowRight")
        await pg.wait_for_timeout(1200)
        b = os.path.join(shots, slug + ".after.png")
        await pg.screenshot(path=b)
        r["reacts"] = moved(pa, frame(b)[0])
        r["shot"] = a
    except Exception as e:
        r["fatal"] = str(e)[:200]
    # A 404 page still has colour — the edge's "Not found" scored 144 the first
    # time this gate ran and passed three hosts that do not exist. Rendering is
    # necessary, not sufficient: the document itself has to have been served.
    r["runs"] = r.get("http", 0) < 400 and r.get("colors", 0) >= BLANK and not r.get("fatal")
    await br.close()
    return r


async def main(argv):
    out = None
    if argv and argv[0] == "--json":
        out, argv = argv[1], argv[2:]
    if not argv:
        print(__doc__)
        return 2
    shots = os.environ.get("SHOTS", os.path.expanduser("~/.cache/hanzo-templates/shots"))
    os.makedirs(shots, exist_ok=True)
    urls = [a if a.startswith("http") else HOST % a for a in argv]
    from playwright.async_api import async_playwright
    async with async_playwright() as pw:
        sem = asyncio.Semaphore(int(os.environ.get("PAR", "4")))

        async def go(u):
            async with sem:
                return await prove(pw, u, shots)
        res = await asyncio.gather(*[go(u) for u in urls])
    dead = 0
    for r in sorted(res, key=lambda x: x["url"]):
        host = r["url"].split("//")[-1].rstrip("/")
        if r["runs"]:
            print("RUNS   %-44s colours=%-6d frames=%-4d reacts=%s%s"
                  % (host, r["colors"], r.get("frames", 0), r.get("reacts", 0),
                     "  isolated" if r.get("coi") else ""))
        else:
            dead += 1
            print("DEAD   %-44s %s" % (host, r.get("fatal") or
                                       ("http %d" % r["http"] if r.get("http", 0) >= 400 else
                                        "rendered %d colours" % r.get("colors", 0))))
        for b in r["bad"][:3]:
            print("       404/5xx %s" % b)
        for e in r["errors"][:2]:
            print("       js: %s" % e)
    print("VERIFY runs=%d dead=%d" % (len(res) - dead, dead))
    if out:
        json.dump(res, open(out, "w"), indent=1)
    return dead


if __name__ == "__main__":
    sys.exit(asyncio.run(main(sys.argv[1:])))
