'use strict';

// The installed web app's service worker. Airwaves is live TV from a server
// on the network, so nothing is kept to work offline: pages, scripts and
// streams always come from the server, as they do in a browser tab. The
// worker is here because an installable app has one, and so that when the
// server can't be reached the app says so instead of the browser's error.

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

self.addEventListener('fetch', (e) => {
  if (e.request.mode !== 'navigate') return; // everything else as usual
  e.respondWith(fetch(e.request).catch(() => new Response(OFFLINE, { headers: { 'Content-Type': 'text/html; charset=utf-8' } })));
});

const OFFLINE = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Airwaves</title>
<body style="margin:0;min-height:100vh;display:grid;place-items:center;background:#07080a;color:#ede6d6;font:16px/1.5 system-ui,sans-serif;text-align:center">
<div><div style="font:800 48px/1 system-ui,sans-serif;letter-spacing:.02em">AIRWAVES</div>
<p style="color:#908a7c;max-width:28em;margin:16px auto">Can't reach the Airwaves server. Check that this device is on the same network, or on Tailscale, and that the server is running.</p>
<button onclick="location.reload()" style="font:600 14px system-ui,sans-serif;letter-spacing:.1em;text-transform:uppercase;padding:12px 20px;border:0;border-radius:3px;background:#ffb224;color:#07080a">Try again</button></div>`;
