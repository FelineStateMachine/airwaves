// Renders the Steam library art PNGs from the HTML sources next to this file.
//
//   node build/linux/steam-art/render.mjs            (needs the playwright package resolvable)
//   PLAYWRIGHT=/path/to/node_modules/playwright/index.mjs node build/linux/steam-art/render.mjs
//
// Serves the repo over a local HTTP server so the pages can load the app's fonts and art.js.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { dirname, extname, join, normalize } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..", "..", "..");
const spec = process.env.PLAYWRIGHT;
const { chromium } = await import(spec ? pathToFileURL(spec).href : "playwright");

// name: [page, width, height, transparent]; width/height null = size of #art
const ART = {
  capsule: ["capsule.html", 600, 900, false],
  header: ["header.html", 920, 430, false],
  hero: ["hero.html", 3840, 1240, false],
  logo: ["logo.html", null, null, true],
  icon: ["icon.html", 256, 256, true],
};

const TYPES = { ".html": "text/html", ".css": "text/css", ".js": "text/javascript", ".woff2": "font/woff2", ".png": "image/png" };
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
const base = `http://127.0.0.1:${server.address().port}/build/linux/steam-art/`;

const only = process.argv.slice(2);
const browser = await chromium.launch();
try {
  for (const [name, [page, w, h, transparent]] of Object.entries(ART)) {
    if (only.length && !only.includes(name)) continue;
    const ctx = await browser.newContext({ viewport: { width: w ?? 2400, height: h ?? 800 } });
    const tab = await ctx.newPage();
    tab.on("pageerror", (e) => console.error(`${name}: ${e.message}`));
    tab.on("requestfailed", (r) => console.error(`${name}: failed ${r.url()}`));
    await tab.goto(base + page);
    await tab.waitForSelector("body[data-ready]", { timeout: 15000 });
    const out = join(here, `${name}.png`);
    await tab.locator("#art").screenshot({ path: out, omitBackground: transparent });
    const box = await tab.locator("#art").boundingBox();
    console.log(`${name}.png ${Math.round(box.width)}x${Math.round(box.height)}`);
    await ctx.close();
  }
} finally {
  await browser.close();
  server.close();
}
