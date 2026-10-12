'use strict';

// Captions, drawn by the app, and audio tracks.

// ---------- captions ----------
// Captions follow the saved setting, except that programs whose sound
// isn't English start with them on. C during such a program turns them
// off or on for it alone, leaving the setting as it was.
const ENGLISH = /^(en|eng|english)([-_].*)?$/i;
const UNKNOWN = /^(|und|unknown|mul|mis|zxx|qaa)$/i;
const foreignAudio = (p) => !!p && !!p.audioLang && !ENGLISH.test(p.audioLang) && !UNKNOWN.test(p.audioLang);

function programOn() {
  if (state.recording || !state.current) return null;
  return airingAt(state.current, Date.now()) || null;
}

const programKey = (p) => `${state.current && state.current.key}|${p.start}`;

function captionsWanted() {
  const p = programOn();
  if (foreignAudio(p)) {
    const o = state.captionsFor;
    return o && o.key === programKey(p) ? o.on : true;
  }
  return !!state.settings.captions;
}

// The app draws captions itself, the same on macOS and Linux, from the
// stream's text track, which it keeps hidden from the webview: a custom
// channel's WebVTT subtitles (timed word by word when YouTube's speech
// recognition made them), else a broadcast's CEA-608 captions as hls.js or
// WebKit decode them. They sit low on the picture, above the banner while
// it shows, sized to the picture and the interface size: at most two
// lines, as even as they'll go, broken where the source broke them when
// that fits. A caption too long for two lines is paged, each page filling
// the box in turn, and timed words appear as they're said. Pretext
// (vendor/pretext) measures the words; canvas does until it has loaded,
// or if it can't.
const CAP_FAMILY = '"Schibsted", "Helvetica Neue", sans-serif';
const CAP_HEIGHT = 0.046; // font size, of the picture's height at 100%
const CAP_EMS = 21;       // the widest line, in ems: about 42 characters
const cap = {
  el: $('#captions'),
  track: null,      // the text track drawn
  timedTrack: false, // whether it times words
  checked: 0,       // how many of its cues that's from
  m: null,          // sizes: { size, max, bottom, pad, font, key }
  key: '',          // the page drawn, at its size
  page: null,
  shown: -1,        // how many of its words show
  spans: [],        // its word elements, timed pages only
  bars: [],
  logical: null,    // the track's cues, whole: { sig, cues }
  timed: null,      // a timed track's words, paged
  frozen: new Map(),// timed pages shown, as they were laid out
  units: new Map(), // untimed captions, paged, by cue
  raf: 0,
  ready: false,     // the caption font has loaded
};
let pretext = null;
import('../vendor/pretext/rich-inline.js') // from this file's folder
  .then((m) => { pretext = m; resetCaptionLayout(); })
  .catch((e) => log('info', `captions measured with canvas: ${e}`));
document.fonts.load(`500 32px ${CAP_FAMILY}`).catch(() => {}).then(() => { cap.ready = true; resetCaptionLayout(); });

function captionTracks() {
  return [...(video.textTracks || [])].filter((t) => t.kind === 'captions' || t.kind === 'subtitles');
}

// applyCaptions shows or hides captions as wanted, from the text subtitles
// when there are some (a custom channel has those and the same words as
// 608 in the picture), else the captions in the picture. It reports
// whether the stream has any.
function applyCaptions() {
  const { tracks, pick, on } = captionPlan();
  for (const t of tracks) {
    const mode = on && t === pick ? 'hidden' : 'disabled';
    if (t.mode !== mode) t.mode = mode;
  }
  // hls.js stops fetching subtitles nobody's reading.
  if (!on && state.hls && state.hls.subtitleTrack !== -1) state.hls.subtitleTrack = -1;
  setCaptionTrack(on ? pick : null);
  return !!pick;
}

// captionPlan is which tracks there are, which one captions come from,
// and whether they're wanted.
function captionPlan() {
  const tracks = captionTracks();
  const pick = tracks.find((t) => t.kind === 'subtitles') || tracks[0] || null;
  return { tracks, pick, on: !!state.settings && captionsWanted() };
}

// Only the app turns captions on: when a player or the webview changes a
// track (picking one itself, or following a system caption setting), the
// app sets them back as wanted.
function keepCaptions() {
  const { tracks, pick, on } = captionPlan();
  if (tracks.some((t) => t.mode !== (on && t === pick ? 'hidden' : 'disabled'))) applyCaptions();
}

async function toggleCaptions() {
  const p = programOn();
  if (foreignAudio(p)) state.captionsFor = { key: programKey(p), on: !captionsWanted() };
  else await saveAppSettings({ ...state.settings, captions: !state.settings.captions });
  const found = applyCaptions();
  toast(captionsWanted() ? (found ? 'Captions on' : 'Captions on (this program has none)') : 'Captions off', 1800);
}

function setCaptionTrack(t) {
  if (t === cap.track) return;
  cap.track = t;
  cap.timedTrack = false;
  cap.checked = 0;
  cap.logical = null;
  cap.timed = null;
  cap.frozen.clear();
  cap.units.clear();
  drawCaption(null);
  if (t && !cap.raf) cap.raf = requestAnimationFrame(captionFrame);
}

// resetCaptionLayout lays captions out again: the picture, the banner or
// the font changed.
function resetCaptionLayout() {
  cap.m = null;
  cap.key = '';
}

function captionFrame() {
  cap.raf = 0;
  if (!cap.track) return;
  if (state.view === 'tv' && cap.ready) renderCaption(video.currentTime);
  cap.raf = requestAnimationFrame(captionFrame);
}

// captionSizes fits captions to the picture within the stage, in CSS
// pixels: the font a share of the picture's height, scaled with the
// interface, and the lines' bottom a little above the picture's, or above
// the banner or the weather crawl.
function captionSizes() {
  const w = stage.clientWidth;
  const h = stage.clientHeight;
  if (!w || !h) return null;
  const ar = video.videoWidth && video.videoHeight ? video.videoWidth / video.videoHeight : 16 / 9;
  const pw = Math.min(w, h * ar);
  const ph = pw / ar;
  // Larger with the interface, though only half as fast: at 150% they'd
  // otherwise crowd the picture.
  const z = 1 + (((state.settings && state.settings.scale) || autoScale()) / 100 - 1) / 2;
  const size = Math.max(12, Math.round(ph * CAP_HEIGHT * z));
  const max = Math.round(Math.min(pw * 0.88, size * CAP_EMS));
  let bottom = (h - ph) / 2 + ph * 0.055;
  const banner = $('#banner');
  if (state.view === 'tv' && banner.classList.contains('show')) bottom = Math.max(bottom, h - banner.offsetTop + size * 0.45);
  const crawl = $('#crawl');
  if (crawl && document.body.classList.contains('has-crawl')) bottom = Math.max(bottom, crawl.offsetHeight + size * 0.45);
  return { size, max, bottom: Math.round(bottom), pad: size * 0.3, font: `500 ${size}px ${CAP_FAMILY}`, key: `${size}/${max}` };
}

function renderCaption(t) {
  if (!cap.m) {
    cap.m = captionSizes();
    if (!cap.m) return;
    cap.el.style.fontSize = `${cap.m.size}px`;
    cap.el.style.bottom = `${cap.m.bottom}px`;
  }
  const track = cap.track;
  const n = track.cues ? track.cues.length : 0;
  if (!cap.timedTrack && n !== cap.checked) {
    cap.timedTrack = timesWords(track);
    cap.checked = n;
  }
  const at = cap.timedTrack ? timedPage(track, t) : untimedPage(track, t);
  drawCaption(at, t);
}

// ----- cue text -----
const VTT_TIME = /^<(?:(\d+):)?(\d\d):(\d\d)\.(\d{3})>$/;
const CUE_ENTITIES = { amp: '&', lt: '<', gt: '>', nbsp: ' ', quot: '"', apos: "'", lrm: '', rlm: '' };
const decodeCue = (s) => s.replace(/&(#x[0-9a-f]+|#\d+|[a-z]+);/gi, (all, e) => {
  if (e[0] !== '#') return CUE_ENTITIES[e.toLowerCase()] ?? all;
  return String.fromCodePoint(e[1] === 'x' || e[1] === 'X' ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10));
}).replace(/¶/g, '♪'); // a music note, as captions made from broadcast ones misname it
const parsedCues = new Map(); // by start and text

function cueText(cue) {
  if (typeof cue.text === 'string') return cue.text;
  return cue.getCueAsHTML ? cue.getCueAsHTML().textContent : '';
}

// logicalCues is a track's cues as the stream sent them, by start. WebKit
// splits a cue that shows alongside others into pieces with its text, one
// for each stretch the set showing stays the same; they're joined again.
function logicalCues(track) {
  const list = track.cues;
  const n = list ? list.length : 0;
  const sig = n ? `${n}|${list[0].startTime}|${list[n - 1].startTime}` : '';
  if (cap.logical && cap.logical.sig === sig) return cap.logical.cues;
  const cues = [];
  const open = new Map();
  for (let i = 0; i < n; i++) {
    const c = list[i];
    const text = cueText(c);
    const l = open.get(text);
    if (l && c.startTime <= l.endTime + 0.05) {
      l.endTime = Math.max(l.endTime, c.endTime);
      continue;
    }
    const cue = { startTime: c.startTime, endTime: c.endTime, text, line: c.line };
    open.set(text, cue);
    cues.push(cue);
  }
  cap.logical = { sig, cues };
  return cues;
}

// cueLines reads a cue into lines of words, each with when it's said:
// the cue's start, or the WebVTT timestamp before it.
function cueLines(cue) {
  const id = `${cue.startTime}\n${cue.text}`;
  let lines = parsedCues.get(id);
  if (lines) {
    for (const l of lines) for (const w of l) w.end = cue.endTime;
    return lines;
  }
  if (parsedCues.size > 5000) parsedCues.clear();
  lines = [];
  for (const raw of cue.text.split(/\r?\n/)) {
    let at = cue.startTime;
    const words = [];
    for (const part of raw.split(/(<[^>]*>)/)) {
      if (part[0] === '<') {
        const m = VTT_TIME.exec(part);
        if (m) at = (+m[1] || 0) * 3600 + +m[2] * 60 + +m[3] + +m[4] / 1000;
        continue;
      }
      for (const w of decodeCue(part).split(/\s+/)) {
        if (w) words.push({ text: w, at, end: cue.endTime });
      }
    }
    if (words.length) lines.push(words);
  }
  parsedCues.set(id, lines);
  return lines;
}

// timesWords reports whether a track's captions time their words, as
// speech recognition's do (though not every cue: a one-word line has no
// time but its start).
function timesWords(track) {
  for (let i = 0; i < Math.min(track.cues.length, 40); i++) {
    if (/<(\d+:)?\d\d:\d\d\.\d{3}>/.test(cueText(track.cues[i]))) return true;
  }
  return false;
}

// ----- measuring -----
const capWidths = new Map(); // font and word: width
let capCanvas = null;

function canvasFont(font) {
  if (!capCanvas) capCanvas = document.createElement('canvas').getContext('2d');
  capCanvas.font = font;
  return capCanvas;
}

// measureWords returns the width of each word in font.
function measureWords(words, font) {
  if (capWidths.size > 20000) capWidths.clear();
  const out = new Array(words.length);
  const todo = [];
  words.forEach((w, i) => {
    const v = capWidths.get(`${font}\n${w}`);
    if (v === undefined) todo.push(i); else out[i] = v;
  });
  if (todo.length && pretext) {
    const p = pretext.prepareRichInline(todo.map((i, k) => ({ text: (k ? ' ' : '') + words[i], font, break: 'never' })));
    pretext.walkRichInlineLineRanges(p, 1e9, (range) => {
      for (const fr of pretext.materializeRichInlineLineRange(p, range).fragments) out[todo[fr.itemIndex]] = fr.occupiedWidth;
    });
  }
  for (const i of todo) {
    if (out[i] === undefined) out[i] = canvasFont(font).measureText(words[i]).width;
    capWidths.set(`${font}\n${words[i]}`, out[i]);
  }
  return out;
}

const spaceWidth = (font) => measureWords(['a\u00a0a', 'aa'], font).reduce((x, y) => x - y);

// ----- laying out -----
const SENTENCE_END = /[.!?…♪]["'”’)\]]*$/;
const CLAUSE_END = /[,;:.!?…—]["'”’)\]]*$/;

// pageWords splits words from..to into pages of at most two lines no
// wider than max, each as full as it goes but ending a sentence where one
// ends late in it, and the last not left with a word or two. A word
// marked brk starts a line (a change of speaker).
function pageWords(words, widths, space, max, from = 0, to = words.length) {
  // fill is where a page from i ends when filled.
  const fill = (i) => {
    let line = 1;
    let start = i;
    let w = 0;
    for (let j = i; j < to; j++) {
      if (j > start && (words[j].brk || w + space + widths[j] > max)) {
        if (line === 2) return j;
        line = 2;
        start = j;
        w = widths[j];
      } else {
        w += (j > start ? space : 0) + widths[j];
      }
    }
    return to;
  };
  const ranges = [];
  for (let i = from; i < to;) {
    let end = fill(i);
    if (end < to) {
      for (let k = end - 1; k > i + (end - i) / 2; k--) {
        if (SENTENCE_END.test(words[k].text)) { end = k + 1; break; }
      }
    }
    ranges.push([i, end]);
    i = end;
  }
  const chars = (a, b) => { let n = 0; for (let i = a; i < b; i++) n += words[i].text.length + 1; return n; };
  const n = ranges.length;
  if (n > 1 && chars(...ranges[n - 1]) < 0.35 * chars(...ranges[n - 2])) {
    // Even out the last two pages, splitting after punctuation if near.
    const [a] = ranges[n - 2];
    const b = ranges[n - 1][1];
    let best = Infinity;
    for (let k = a + 1; k < b; k++) {
      if (fill(a) < k || fill(k) < b) continue;
      const c = Math.abs(chars(a, k) - chars(k, b)) - (CLAUSE_END.test(words[k - 1].text) ? 8 : 0);
      if (c < best) { best = c; ranges[n - 2] = [a, k]; ranges[n - 1] = [k, b]; }
    }
  }
  return ranges.map(([a, b]) => balance(words, widths, space, max, a, b));
}

// balance puts words a..b on one line if they fit, else two of about
// equal width, breaking after punctuation where it can, at a change of
// speaker where there is one.
function balance(words, widths, space, max, a, b) {
  const sum = [0];
  for (let i = a; i < b; i++) sum.push(sum[sum.length - 1] + widths[i] + space);
  const width = (x, y) => sum[y - a] - sum[x - a] - space;
  for (let k = a + 1; k < b; k++) {
    if (words[k].brk) return { from: a, to: b, lines: [[a, k], [k, b]] };
  }
  if (width(a, b) <= max || b - a < 2) return { from: a, to: b, lines: [[a, b]] };
  let best = Infinity;
  let at = a + 1;
  for (let k = a + 1; k < b; k++) {
    const l = width(a, k);
    const r = width(k, b);
    if (l > max || r > max) continue;
    const c = Math.abs(l - r) + (l > r ? space : 0) - (CLAUSE_END.test(words[k - 1].text) ? 3 * space : 0);
    if (c < best) { best = c; at = k; }
  }
  return { from: a, to: b, lines: [[a, at], [at, b]] };
}

// speakerMark reports whether a word marks a change of speaker, as
// speech recognition and broadcast captions do with ">>".
const speakerMark = (w) => w.startsWith('>>');

// ----- choosing what shows -----
// cuesAt lists a track's cues showing at t, top row first.
function cuesAt(track, t) {
  const list = logicalCues(track);
  let lo = 0;
  let hi = list.length - 1;
  let i = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (list[mid].startTime <= t) { i = mid; lo = mid + 1; } else hi = mid - 1;
  }
  const out = [];
  for (let k = i; k >= 0 && k > i - 40; k--) if (list[k].endTime > t) out.push(list[k]);
  const row = (c) => (typeof c.line === 'number' ? c.line : 0);
  return out.sort((x, y) => row(x) - row(y) || x.startTime - y.startTime);
}

// untimedPage is what shows at t of captions without word times: the
// cues showing, laid out together and paged over their time.
function untimedPage(track, t) {
  const cues = cuesAt(track, t);
  if (!cues.length) return null;
  const key = cues.map((c) => `${c.startTime}/${c.text}`).join('\n');
  let u = cap.units.get(key);
  if (!u || u.end !== Math.max(...cues.map((c) => c.endTime))) {
    if (cap.units.size > 200) cap.units.clear();
    const m = cap.m;
    // Overlapping cues showing the same line show it once, oldest first.
    const seen = new Set();
    const lines = cues.flatMap(cueLines).filter((l) => {
      const text = l.map((w) => w.text).join(' ');
      return !seen.has(text) && seen.add(text);
    });
    const words = [];
    for (const [i, l] of lines.entries()) {
      for (const [k, w] of l.entries()) words.push({ ...w, brk: (k === 0 && i > 0 && /^[-–—]/.test(w.text)) || speakerMark(w.text) });
    }
    const widths = measureWords(words.map((w) => w.text), m.font);
    const space = spaceWidth(m.font);
    // Lines that fit stay as the source broke them.
    let pages;
    const lineWidth = (from, n) => widths.slice(from, from + n).reduce((s, x) => s + x, 0) + (n - 1) * space;
    let from = 0;
    if (lines.length <= 2 && lines.every((l) => { const ok = lineWidth(from, l.length) <= m.max; from += l.length; return ok; })) {
      const cut = lines[0].length;
      pages = [{ from: 0, to: words.length, lines: lines.length === 2 ? [[0, cut], [cut, words.length]] : [[0, words.length]] }];
    } else {
      pages = pageWords(words, widths, space, m.max);
    }
    // Pages share the cues' time out by length.
    const start = Math.min(...cues.map((c) => c.startTime));
    const end = Math.max(...cues.map((c) => c.endTime));
    const chars = (p) => words.slice(p.from, p.to).reduce((s, w) => s + w.text.length + 1, 0);
    const total = pages.reduce((s, p) => s + chars(p), 0);
    let done = 0;
    for (const [i, p] of pages.entries()) {
      p.start = start + ((end - start) * done) / total;
      done += chars(p);
      p.end = i === pages.length - 1 ? end : start + ((end - start) * done) / total;
      p.id = `${key}#${i}`;
    }
    u = { words, widths, space, pages, end };
    cap.units.set(key, u);
  }
  const page = u.pages.find((p) => p.start <= t && t < p.end) || u.pages[u.pages.length - 1];
  return { page, words: u.words, widths: u.widths, space: u.space };
}

// timedPage is what shows at t of captions timed word by word: the
// track's words, in runs of speech split at pauses, paged; a page shows
// from its first word until the next page's, its words as they're said.
// Captions arrive a little ahead of the picture, so a page is laid out
// with what's known then; once it shows it stays as it is (frozen), and
// words that come later go on the pages after.
const wordKey = (w) => `${w.at}\n${w.text}`;

function timedPage(track, t) {
  const m = cap.m;
  const list = logicalCues(track);
  if (!list.length) return null;
  const sig = `${cap.logical.sig}|${m.key}`;
  if (!cap.timed || cap.timed.sig !== sig) {
    const all = [];
    // A cue's last line is its own: the server's screens roll up, the
    // line above repeating the cue before's, which other sources overlap.
    for (const c of list) {
      const lines = cueLines(c);
      if (lines.length) all.push(...lines[lines.length - 1]);
    }
    all.sort((x, y) => x.at - y.at);
    const words = [];
    for (const w of all) {
      const last = words[words.length - 1];
      if (last && last.at === w.at && last.text === w.text) last.end = Math.max(last.end, w.end);
      else words.push({ ...w, brk: speakerMark(w.text) });
    }
    const widths = measureWords(words.map((w) => w.text), m.font);
    const space = spaceWidth(m.font);
    const pages = [];
    for (let a = 0; a < words.length;) {
      let b = a + 1;
      while (b < words.length && words[b].at <= words[b - 1].end + 0.25 && words[b].at - words[b - 1].at < 4) b++;
      const run = [];
      let i = a;
      // Pages already shown, as they were.
      for (let f = cap.frozen.get(wordKey(words[i])); f && i < b; f = i < b && cap.frozen.get(wordKey(words[i]))) {
        let j = i;
        while (j < b && wordKey(words[j]) !== f.last) j++;
        if (j === b) break;
        let k = -1;
        for (let x = i + 1; x <= j; x++) if (f.brk && wordKey(words[x]) === f.brk) k = x;
        run.push({ from: i, to: j + 1, lines: k > 0 ? [[i, k], [k, j + 1]] : [[i, j + 1]] });
        i = j + 1;
      }
      if (i < b) run.push(...pageWords(words, widths, space, m.max, i, b));
      for (const [k, p] of run.entries()) {
        p.start = words[p.from].at;
        let end = 0;
        for (let x = p.from; x < p.to; x++) end = Math.max(end, words[x].end);
        p.end = k + 1 < run.length ? words[run[k + 1].from].at : Math.max(end, p.start + 1);
        p.id = `${wordKey(words[p.from])}/${p.to - p.from}`;
        pages.push(p);
      }
      a = b;
    }
    cap.timed = { sig, words, widths, space, pages };
  }
  const { pages, words } = cap.timed;
  let lo = 0;
  let hi = pages.length - 1;
  let i = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (pages[mid].start <= t) { i = mid; lo = mid + 1; } else hi = mid - 1;
  }
  if (i < 0 || t >= pages[i].end) return null;
  const page = pages[i];
  if (!cap.frozen.has(wordKey(words[page.from]))) {
    if (cap.frozen.size > 400) cap.frozen.delete(cap.frozen.keys().next().value);
    const [, second] = page.lines;
    cap.frozen.set(wordKey(words[page.from]), { last: wordKey(words[page.to - 1]), brk: second ? wordKey(words[second[0]]) : '' });
  }
  return { page, words, widths: cap.timed.widths, space: cap.timed.space, timed: true };
}

// ----- drawing -----
function drawCaption(at, t) {
  const key = at ? `${at.page.id}@${cap.m.key}` : '';
  if (key !== cap.key) {
    cap.key = key;
    cap.page = at;
    cap.shown = -1;
    cap.spans = [];
    cap.bars = [];
    cap.el.textContent = '';
    if (!at) return;
    for (const [a, b] of at.page.lines) {
      const line = document.createElement('div');
      line.className = 'cap-line';
      const bar = document.createElement('span');
      bar.className = at.timed ? 'cap-bar timed' : 'cap-bar';
      if (at.timed) {
        for (let i = a; i < b; i++) {
          const s = document.createElement('span');
          s.textContent = (i > a ? ' ' : '') + at.words[i].text;
          bar.append(s);
          cap.spans.push({ el: s, i, bar, a });
        }
        cap.bars.push({ bar, a, b });
      } else {
        bar.textContent = at.words.slice(a, b).map((w) => w.text).join(' ');
      }
      line.append(bar);
      cap.el.append(line);
    }
  }
  if (!at || !at.timed) return;
  // Timed words show as they're said, with the bar behind them growing.
  const { page, words, widths, space } = at;
  let n = page.from;
  while (n < page.to && words[n].at <= t) n++;
  if (n === cap.shown) return;
  cap.shown = n;
  for (const s of cap.spans) s.el.classList.toggle('wait', s.i >= n);
  for (const { bar, a, b } of cap.bars) {
    let w = 0;
    for (let i = a; i < Math.min(b, n); i++) w += widths[i] + (i > a ? space : 0);
    bar.style.setProperty('--r', n > a ? `${Math.ceil(w + 2 * cap.m.pad + 2)}px` : '0px');
  }
}

// A new stream: the last one's captions go at once, not when its tracks do.
video.addEventListener('emptied', () => setCaptionTrack(null));
if (window.ResizeObserver) new ResizeObserver(resetCaptionLayout).observe(stage);
video.addEventListener('resize', resetCaptionLayout);
new MutationObserver(resetCaptionLayout).observe($('#banner'), { attributes: true, attributeFilter: ['class'] });
new MutationObserver(resetCaptionLayout).observe(document.body, { attributes: true, attributeFilter: ['class'] });

function audioTracks() {
  if (state.hls) return state.hls.audioTracks.map((t, i) => ({ i, label: t.name, lang: t.lang, on: state.hls.audioTrack === i }));
  const list = video.audioTracks;
  if (!list) return [];
  const out = [];
  for (let i = 0; i < list.length; i++) out.push({ i, label: list[i].label, lang: list[i].language, on: list[i].enabled });
  return out;
}

function selectAudio(i) {
  if (state.hls) { state.hls.audioTrack = i; return; }
  const list = video.audioTracks;
  for (let j = 0; j < list.length; j++) list[j].enabled = j === i;
}

// applyAudio picks the preferred language when the stream offers it.
function applyAudio() {
  const want = state.settings.audioLang;
  if (!want) return;
  const t = audioTracks().find((x) => (x.lang || '').startsWith(want));
  if (t && !t.on) selectAudio(t.i);
}

async function cycleAudio() {
  const tracks = audioTracks();
  if (tracks.length < 2) return toast('Only one audio track on this channel', 1800);
  const cur = tracks.findIndex((t) => t.on);
  const next = tracks[(cur + 1) % tracks.length];
  selectAudio(next.i);
  await saveAppSettings({ ...state.settings, audioLang: (next.lang || '').slice(0, 2) });
  toast(`Audio: ${next.label || next.lang || `track ${next.i + 1}`}`, 1800);
}
