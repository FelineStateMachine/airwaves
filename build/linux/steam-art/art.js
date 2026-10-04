// Builds the faint guide-grid motif used across the Steam library art.
// Deterministic (seeded) so re-renders come out identical.

function rng(seed) {
  return () => {
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const CHANNELS = ["2.1", "4.1", "5.1", "7.1", "9.1", "11.1", "13.1", "20.1", "28.1", "32.1", "38.1", "44.1", "50.1", "62.1"];
// Program lengths in half hours: mostly half-hour and hour shows, the odd movie.
const LENGTHS = [1, 1, 1, 1, 2, 2, 2, 1, 3, 4];

function clock(min) {
  const h = Math.floor(min / 60) % 12 || 12;
  const m = String(min % 60).padStart(2, "0");
  return `${h}:${m}`;
}

// o: { u: scale (1 = the app's 58px rows), chw: channel column width in app px (0 = none),
//      slot: px per half hour in app px, now: now position in half hours from the lane start,
//      ticks: show the time row, t0: minutes since midnight at the lane start,
//      first: index into CHANNELS, cur: row index of the tuned channel, sel: highlight its program,
//      seed }
export function guide(el, o) {
  const u = o.u ?? 1;
  el.style.setProperty("--u", u);
  const W = el.clientWidth, H = el.clientHeight;
  const rowH = 58 * u, tickH = o.ticks ? 34 * u : 0, chw = (o.chw ?? 0) * u;
  const slot = (o.slot ?? 180) * u, gap = 3 * u;
  const nowX = chw + (o.now ?? 1.4) * slot;
  const rand = rng(o.seed ?? 7);

  if (o.ticks) {
    const ticks = document.createElement("div");
    ticks.className = "ticks";
    for (let i = 0, x = chw; x < W; i++, x += slot) {
      const s = document.createElement("span");
      s.style.left = `${x}px`;
      s.textContent = clock((o.t0 ?? 19 * 60) + i * 30);
      ticks.appendChild(s);
    }
    el.appendChild(ticks);
  }

  const rows = Math.ceil((H - tickH) / rowH);
  for (let i = 0; i < rows; i++) {
    const row = document.createElement("div");
    row.className = "row" + (i === o.cur ? " cur" : "");
    row.style.top = `${tickH + i * rowH}px`;
    row.style.height = `${rowH}px`;
    if (chw > 0) {
      const ch = document.createElement("div");
      ch.className = "ch";
      ch.style.width = `${chw}px`;
      ch.textContent = CHANNELS[((o.first ?? 0) + i) % CHANNELS.length];
      row.appendChild(ch);
    }
    // Programs start on half-hour boundaries; the first one usually began before the window.
    let x = chw - Math.floor(rand() * 3) * slot;
    while (x < W) {
      const len = LENGTHS[Math.floor(rand() * LENGTHS.length)] * slot;
      const left = Math.max(x, chw), right = x + len - gap;
      if (right > left) {
        const c = document.createElement("div");
        c.className = "cell";
        if (right < nowX) c.classList.add("past");
        else if (x <= nowX) c.classList.add(i === o.cur && o.sel ? "sel" : "now");
        c.style.left = `${left}px`;
        c.style.width = `${right - left}px`;
        row.appendChild(c);
      }
      x += len;
    }
    el.appendChild(row);
  }

  const line = document.createElement("div");
  line.className = "now-line";
  line.style.left = `${nowX}px`;
  el.appendChild(line);
}

// Resolves once the art's fonts are in. Pages set body[data-ready] when drawn.
export async function ready() {
  await document.fonts.load('800 100px "Shoulders"');
  await document.fonts.load('600 100px "Shoulders"');
  await document.fonts.load('400 12px "Plex Mono"');
  await document.fonts.load('500 12px "Plex Mono"');
  await document.fonts.ready;
}
