// Renders the Android TV app's banner, launcher icon and setup wordmark from
// the HTML sources next to this file, into android/app/src/main/res. They share
// the Steam art's look (build/linux/steam-art: art.css, art.js, the app's fonts).
//
//   node android/art/render.mjs [banner|mark|icon ...]   (needs the playwright package resolvable)
//   PLAYWRIGHT=/path/to/node_modules/playwright/index.mjs node android/art/render.mjs
//
// Serves the repo over a local HTTP server so the pages can load the app's fonts and art.js.

import { createServer } from "node:http";
import { mkdir, readFile } from "node:fs/promises";
import { dirname, extname, join, normalize } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..", "..");
const res = join(here, "..", "app", "src", "main", "res");
const spec = process.env.PLAYWRIGHT;
const { chromium } = await import(spec ? pathToFileURL(spec).href : "playwright");

// The launcher icon's foreground layer is 108dp square, per density.
const ICON = { mdpi: 108, hdpi: 162, xhdpi: 216, xxhdpi: 324, xxxhdpi: 432 };

// name: [page, width, height, transparent, output]; width/height null = size of #art
const ART = {
  banner: ["banner.html", 640, 360, false, "drawable-xhdpi/banner.png"],
  mark: ["mark.html", null, null, true, "drawable-nodpi/mark.png"],
  ...Object.fromEntries(Object.entries(ICON).map(([d, px]) =>
    [`icon-${d}`, [`icon.html?px=${px}`, px, px, true, `mipmap-${d}/ic_launcher_foreground.png`]])),
};

const TYPES = { ".html": "text/html", ".css": "text/css", ".js": "text/javascript", ".mjs": "text/javascript", ".woff2": "font/woff2", ".png": "image/png" };
const server = createServer(async (req, res) => {
  const path = normalize(decodeURIComponent(new URL(req.url, "http://x").pathname));
  try {
    const body = await readFile(join(root, path));
    res.writeHead(200, { "content-type": TYPES[extname(path)] ?? "application/octet-stream" });
    res.end(body);
  } catch {
    res.writeHead(404).end();
  }
});
await new Promise((ok) => server.listen(0, "127.0.0.1", ok));
const base = `http://127.0.0.1:${server.address().port}/android/art/`;

const only = process.argv.slice(2);
const browser = await chromium.launch();
try {
  for (const [name, [page, w, h, transparent, out]] of Object.entries(ART)) {
    if (only.length && !only.some((o) => name === o || name.startsWith(`${o}-`))) continue;
    const ctx = await browser.newContext({ viewport: { width: w ?? 2400, height: h ?? 800 } });
    const tab = await ctx.newPage();
    tab.on("pageerror", (e) => console.error(`${name}: ${e.message}`));
    tab.on("requestfailed", (r) => console.error(`${name}: failed ${r.url()}`));
    await tab.goto(base + page);
    await tab.waitForSelector("body[data-ready]", { timeout: 15000 });
    const file = join(res, out);
    await mkdir(dirname(file), { recursive: true });
    await tab.locator("#art").screenshot({ path: file, omitBackground: transparent });
    const box = await tab.locator("#art").boundingBox();
    console.log(`${out} ${Math.round(box.width)}x${Math.round(box.height)}`);
    await ctx.close();
  }
} finally {
  await browser.close();
  server.close();
}
