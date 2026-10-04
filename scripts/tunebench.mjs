// tunebench measures how long channel changes take on a running airwavesd,
// as the app sees them: the tune call, then playback in headless Chrome
// with hls.js set up as the app sets it up, to the first frame and through
// the first seconds, counting stalls. Tunes run one at a time, as their own
// client, and each is stopped before the next.
//
//   node scripts/tunebench.mjs --server http://nas:8089 --plan 9.1x3,1.2x3,201.0x2
//
// Options:
//   --server URL      the airwavesd to measure (required)
//   --plan LIST       channel numbers, each with "xN" for N tunes; tunes go
//                     round by round through the list, so a channel's tunes
//                     are spread out
//   --watch S         seconds to watch each tune after it plays (30)
//   --gap S           seconds between tunes (5)
//   --client ID       the client ID to tune as ("bench")
//   --token T         the API token, when the server has one
//   --out FILE        append each tune's result to FILE as JSON lines
//   --label NAME      names the run in the results ("run")
//   --chrome PATH     a Chrome or Chromium that plays H.264 (default: the
//                     installed Google Chrome)
//
// Playwright is loaded from PLAYWRIGHT (a path to its index.mjs) or the
// "playwright" package.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const args = parseArgs(process.argv.slice(2));
if (!args.server || !args.plan) {
  console.error('usage: node scripts/tunebench.mjs --server URL --plan 9.1x3,1.2x3 [--watch 30] [--gap 5] [--out FILE]');
  process.exit(2);
}
const server = args.server.replace(/\/+$/, '');
const watch = Number(args.watch ?? 30);
const gap = Number(args.gap ?? 5);
const client = args.client || 'bench';
const label = args.label || 'run';
const headers = { 'Content-Type': 'application/json', ...(args.token ? { Authorization: `Bearer ${args.token}` } : {}) };

const here = path.dirname(fileURLToPath(import.meta.url));
const hlsjs = path.join(here, '..', 'frontend', 'dist', 'vendor', 'hls.min.js');
const { chromium } = await import(process.env.PLAYWRIGHT || 'playwright');

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (!a.startsWith('--')) continue;
    const [k, v] = a.slice(2).split('=', 2);
    out[k] = v ?? argv[++i];
  }
  return out;
}

// plan expands "9.1x3,1.2x2" into tunes, round by round.
function plan(spec) {
  const want = spec.split(',').map((s) => s.trim()).filter(Boolean).map((s) => {
    const [number, n] = s.split('x');
    return { number, n: Number(n || 1) };
  });
  const out = [];
  for (let round = 0; want.some((w) => w.n > round); round++) {
    for (const w of want) if (w.n > round) out.push(w.number);
  }
  return out;
}

async function api(method, p, body) {
  const r = await fetch(server + p, { method, headers, body: body ? JSON.stringify(body) : undefined });
  const text = await r.text();
  let json;
  try { json = JSON.parse(text); } catch { json = { error: text }; }
  if (!r.ok) throw new Error(`${p}: HTTP ${r.status}: ${json.error || text}`.slice(0, 300));
  return json;
}

async function kinds() {
  const snap = await api('GET', '/api/snapshot');
  const m = new Map();
  for (const c of snap.custom || []) m.set(c.number, c.kind || 'folder');
  return m;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const quantile = (xs, q) => {
  const s = xs.filter((x) => x != null && Number.isFinite(x)).sort((a, b) => a - b);
  if (!s.length) return null;
  return s[Math.min(s.length - 1, Math.max(0, Math.ceil(q * s.length) - 1))];
};
const secs = (ms) => (ms == null ? '-' : (ms / 1000).toFixed(2));

// The page plays a stream as the app does (frontend/dist/app.js attempt()),
// and keeps a record of what happened when.
const pageScript = () => {
  window.bench = (src, watchMs) => new Promise((resolve) => {
    const video = document.querySelector('video');
    const t0 = Date.now();
    const rec = { start: t0, events: {}, stalls: [], errors: [], buffers: [] };
    const mark = (name) => { if (rec.events[name] == null) rec.events[name] = Date.now() - t0; };
    const h = new window.Hls({ liveSyncDurationCount: 2, maxBufferLength: 12, subtitleDisplay: false });
    const E = window.Hls.Events;
    h.on(E.MANIFEST_PARSED, (_, d) => { mark('manifest'); rec.audioTracks = (d.audioTracks || []).length; rec.subtitleTracks = (d.subtitleTracks || []).length; });
    h.on(E.LEVEL_LOADED, (_, d) => { mark('level'); if (rec.firstLevel == null) rec.firstLevel = { segments: d.details.fragments.length, targetDuration: d.details.targetduration, first: d.details.fragments.map((f) => +f.duration.toFixed(2)).slice(0, 4) }; });
    h.on(E.FRAG_LOADED, (_, d) => { if (d.frag.type === 'main') mark('fragLoaded'); });
    h.on(E.FRAG_BUFFERED, (_, d) => { if (d.frag.type === 'main') mark('fragBuffered'); });
    h.on(E.BUFFER_CREATED, (_, d) => { rec.buffers = Object.keys(d.tracks || {}); });
    h.on(E.ERROR, (_, d) => { rec.errors.push(`${d.fatal ? 'fatal ' : ''}${d.details}`); });
    let playing = false;
    let stallAt = 0;
    video.addEventListener('playing', () => {
      if (!playing) { playing = true; mark('playing'); rec.startPos = video.currentTime; rec.liveEdge = h.liveSyncPosition; }
      if (stallAt) { rec.stalls[rec.stalls.length - 1].ms = Date.now() - stallAt; stallAt = 0; }
    });
    video.addEventListener('waiting', () => {
      if (!playing || stallAt) return;
      stallAt = Date.now();
      rec.stalls.push({ at: stallAt - t0, ms: null });
    });
    // A picture that stops without a waiting event counts too.
    let lastPos = -1;
    let still = 0;
    const tick = setInterval(() => {
      if (!playing || video.paused) return;
      if (video.currentTime === lastPos && !stallAt) {
        if (++still === 2) rec.stalls.push({ at: Date.now() - t0 - 1000, ms: null, frozen: true });
      } else still = 0;
      lastPos = video.currentTime;
    }, 500);
    video.muted = true;
    h.loadSource(src);
    h.attachMedia(video);
    video.play().catch(() => {});
    const giveUp = setTimeout(() => finish(), watchMs + 20000);
    const finish = () => {
      clearInterval(tick);
      clearTimeout(giveUp);
      if (stallAt) rec.stalls[rec.stalls.length - 1].ms = Date.now() - stallAt;
      const q = video.getVideoPlaybackQuality ? video.getVideoPlaybackQuality() : {};
      rec.dropped = q.droppedVideoFrames;
      rec.frames = q.totalVideoFrames;
      rec.width = video.videoWidth;
      rec.latency = h.latency;
      rec.played = video.currentTime - (rec.startPos || 0);
      h.destroy();
      resolve(rec);
    };
    // Watch for watchMs after playing starts, or give up 20 s after that.
    const wait = setInterval(() => {
      if (rec.events.playing != null && Date.now() - t0 - rec.events.playing >= watchMs) { clearInterval(wait); finish(); }
    }, 200);
  });
};

const browser = await chromium.launch({
  ...(args.chrome ? { executablePath: args.chrome } : { channel: 'chrome' }),
  args: ['--autoplay-policy=no-user-gesture-required', '--mute-audio'],
});
const page = await browser.newPage();
await page.setContent('<!doctype html><video width="640" height="360"></video>');
await page.addScriptTag({ path: hlsjs });
await page.evaluate(pageScript);

const kindOf = await kinds();
const tunes = plan(args.plan);
const results = [];
console.log(`${tunes.length} tunes on ${server}, ${watch} s each, ${gap} s apart`);
for (const [i, number] of tunes.entries()) {
  if (i > 0) await sleep(gap * 1000);
  const kind = kindOf.get(number) || 'antenna';
  const r = { label, number, kind, at: new Date().toISOString() };
  const t0 = Date.now();
  try {
    const pb = await api('POST', '/api/tune', { client, number });
    r.api = Date.now() - t0;
    r.timing = pb.timing;
    const rec = await page.evaluate(([src, ms]) => window.bench(src, ms), [server + pb.path, watch * 1000]);
    const off = rec.start - t0; // tune call to the page's start
    r.page = rec;
    r.ttff = rec.events.playing != null ? off + rec.events.playing : null;
    if (r.ttff == null) r.error = `never played${rec.errors.length ? ': ' + rec.errors.join(' ') : ''}`;
    r.stalls = rec.stalls.length;
    r.stallMs = rec.stalls.reduce((s, x) => s + (x.ms || 0), 0);
  } catch (e) {
    r.error = String(e.message || e);
  }
  try { await api('POST', '/api/stop', { client }); } catch (e) { r.stopError = String(e.message || e); }
  results.push(r);
  if (args.out) fs.appendFileSync(args.out, JSON.stringify(r) + '\n');
  const ev = r.page ? r.page.events : {};
  const t = r.timing ? ' | server: ' + r.timing.map((m) => `${m.name} ${secs(m.ms)}`).join(', ') : '';
  console.log(`${String(i + 1).padStart(2)} ${number.padEnd(6)} ${kind.padEnd(8)} api ${secs(r.api)} s, first frame ${secs(r.ttff)} s` +
    ` (manifest +${secs(ev.manifest)}, fragment +${secs(ev.fragLoaded)}, playing +${secs(ev.playing)}),` +
    ` stalls ${r.stalls ?? '-'}${r.stallMs ? ` (${secs(r.stallMs)} s)` : ''}${r.page && r.page.errors.length ? ' errors ' + r.page.errors.join(' ') : ''}${r.error ? ' ERROR ' + r.error : ''}${t}`);
}
await browser.close();

// The summary: per kind, the median and worst of each measure.
const byKind = new Map();
for (const r of results) {
  if (!byKind.has(r.kind)) byKind.set(r.kind, []);
  byKind.get(r.kind).push(r);
}
console.log('\nkind      tunes  api p50  api max  frame p50  frame max  stalls  failed');
for (const [kind, rs] of byKind) {
  const ok = rs.filter((r) => !r.error);
  console.log(`${kind.padEnd(9)} ${String(rs.length).padStart(5)}  ${secs(quantile(ok.map((r) => r.api), 0.5)).padStart(7)}  ${secs(quantile(ok.map((r) => r.api), 1)).padStart(7)}` +
    `  ${secs(quantile(ok.map((r) => r.ttff), 0.5)).padStart(9)}  ${secs(quantile(ok.map((r) => r.ttff), 1)).padStart(9)}` +
    `  ${String(ok.reduce((s, r) => s + (r.stalls || 0), 0)).padStart(6)}  ${String(rs.length - ok.length).padStart(6)}`);
}

// The server's steps, when it reports them: per kind, each step's median.
const steps = [...byKind].map(([kind, rs]) => {
  const at = new Map();
  for (const r of rs) for (const m of r.timing || []) {
    if (!at.has(m.name)) at.set(m.name, []);
    at.get(m.name).push(m.ms);
  }
  return [kind, [...at].map(([name, ms]) => `${name} ${secs(quantile(ms, 0.5))}`).join(', ')];
}).filter(([, s]) => s);
if (steps.length) {
  console.log('\nserver steps, median seconds from the tune call');
  for (const [kind, s] of steps) console.log(`${kind.padEnd(9)} ${s}`);
}
