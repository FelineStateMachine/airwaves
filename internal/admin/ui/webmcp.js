// Airwaves channel admin, for AI agents: registers the server's agent tools
// (the ones /admin/mcp serves, internal/admin/mcp.go) with WebMCP, so an
// agent working through this page, in the browser or on this computer by
// way of the WebMCP local relay, manages channels as the page does. Each
// tool calls the server; after a change the page refreshes to show it.
//
// WebMCP needs a secure context, so this only runs with the page opened at
// http://localhost (an SSH tunnel) or over HTTPS; over plain HTTP to the
// tailnet address it does nothing, and agents use /admin/mcp directly.
//
// vendor/mcp-b (MIT, see its LICENSE): @mcp-b/global 5.1.0's
// dist/index.iife.js, which installs document.modelContext, and
// @mcp-b/webmcp-local-relay 5.1.0's dist/browser, which hands the tools to
// `npx @mcp-b/webmcp-local-relay` on 127.0.0.1:9333 when one runs.
'use strict';

(() => {
  const MCP = '/admin/mcp';
  const VENDOR = 'vendor/mcp-b/';
  const RELAY = { 'data-relay-host': '127.0.0.1', 'data-relay-port': '9333', 'data-request-timeout': '120000' };

  let seq = 0;
  async function rpc(method, params) {
    const res = await fetch(MCP, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream', 'X-Airwaves-Via': 'webmcp' },
      body: JSON.stringify({ jsonrpc: '2.0', id: ++seq, method, params }),
    });
    const text = await res.text();
    let msg = null;
    try { msg = JSON.parse(text); } catch { msg = null; }
    if (!res.ok || !msg) throw new Error(`${method}: ${res.status} ${text.trim().slice(0, 200)}`);
    if (msg.error) throw new Error(`${method}: ${msg.error.message}`);
    return msg.result;
  }

  function load(src, attrs = {}) {
    return new Promise((resolve, reject) => {
      const s = document.createElement('script');
      s.src = src;
      for (const [k, v] of Object.entries(attrs)) s.setAttribute(k, v);
      s.onload = resolve;
      s.onerror = () => reject(new Error(`couldn't load ${src}`));
      document.head.append(s);
    });
  }

  // refreshPage has the page take in a change an agent made (admin.js's
  // airwavesAdmin.refresh), following the open channel when the change gave
  // it a new number: from is the number the tool was given, to the one the
  // channel has after (in the tool's result).
  function refreshPage(args, result) {
    let out = null;
    try { out = JSON.parse(((result && result.content) || [])[0].text); } catch { out = null; }
    const ch = out && out.channel;
    const hint = {
      from: args && args.number != null ? String(args.number) : undefined,
      to: ch && ch.number ? String(ch.number) : undefined,
    };
    if (window.airwavesAdmin) window.airwavesAdmin.refresh(hint);
  }

  async function start() {
    if (!window.isSecureContext) {
      console.info(`[airwaves] WebMCP is off: it needs the page at http://localhost or over HTTPS. Agents can use ${location.origin}${MCP} directly.`);
      return;
    }
    // Never for a page that frames this one.
    if (window.top !== window) return;
    // Only this window may call the tools, never a frame's parent.
    window.__webModelContextOptions = { transport: { iframeServer: false, tabServer: { allowedOrigins: [location.origin] } } };
    await load(VENDOR + 'global.iife.js');
    const ctx = document.modelContext;
    if (!ctx || typeof ctx.registerTool !== 'function') throw new Error('document.modelContext is missing');

    const tools = [];
    let cursor;
    do {
      const page = await rpc('tools/list', cursor ? { cursor } : {});
      tools.push(...page.tools);
      cursor = page.nextCursor;
    } while (cursor);
    if (!tools.length) return;
    for (const t of tools) {
      const changes = !(t.annotations && t.annotations.readOnlyHint);
      await ctx.registerTool({
        name: t.name,
        title: t.title,
        description: t.description,
        inputSchema: t.inputSchema,
        annotations: t.annotations,
        execute: async (args) => {
          try {
            const result = await rpc('tools/call', { name: t.name, arguments: args || {} });
            if (changes && !result.isError) refreshPage(args, result);
            return result;
          } catch (e) {
            return { content: [{ type: 'text', text: `${t.name} failed: ${e.message || e}` }], isError: true };
          }
        },
      });
    }
    console.info(`[airwaves] ${tools.length} WebMCP tools registered`);
    await load(VENDOR + 'webmcp-local-relay/embed.js', RELAY);
  }

  start().catch((e) => console.warn('[airwaves] WebMCP tools not registered:', e.message || e));
})();
