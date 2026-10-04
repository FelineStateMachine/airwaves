package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"airwaves/internal/vchan"
)

// agent connects to the harness's agent endpoint as an MCP client would.
func (h *harness) agent() *mcp.ClientSession {
	h.t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: h.url + mcpPath}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { s.Close() })
	return s
}

// call calls a tool, returning its answer decoded, as text, and whether
// it's an error.
func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (map[string]any, string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(text.String()), &m)
	return m, text.String(), res.IsError
}

// ok calls a tool that should work.
func ok(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	m, text, isErr := call(t, s, name, args)
	if isErr || m == nil {
		t.Fatalf("%s %v: %s", name, args, text)
	}
	return m
}

// fails calls a tool that should fail, saying want.
func fails(t *testing.T, s *mcp.ClientSession, name string, args map[string]any, want string) {
	t.Helper()
	if _, text, isErr := call(t, s, name, args); !isErr || !strings.Contains(text, want) {
		t.Errorf("%s %v: want an error with %q, got %v %s", name, args, want, isErr, text)
	}
}

func list(v any) []any { l, _ := v.([]any); return l }

func TestAgentToolList(t *testing.T) {
	h := newHarness(t)
	res, err := h.agent().ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if len(tool.Description) < 80 || tool.Title == "" || tool.Annotations == nil {
			t.Errorf("%s: describe it for agents: %q", tool.Name, tool.Description)
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema map[string]any
		json.Unmarshal(raw, &schema)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%s: schema %s", tool.Name, raw)
		}
		for prop, p := range schema["properties"].(map[string]any) {
			if d, _ := p.(map[string]any)["description"].(string); d == "" {
				t.Errorf("%s.%s has no description", tool.Name, prop)
			}
		}
		readOnly := tool.Annotations.ReadOnlyHint
		if want := strings.HasPrefix(tool.Name, "list_") || strings.HasPrefix(tool.Name, "get_") || strings.HasPrefix(tool.Name, "search_") ||
			strings.HasPrefix(tool.Name, "lookup_") || strings.HasPrefix(tool.Name, "browse_"); readOnly != want {
			t.Errorf("%s: read-only %v", tool.Name, readOnly)
		}
		if destructive := tool.Annotations.DestructiveHint; (tool.Name == "delete_channel") != (destructive != nil && *destructive) {
			t.Errorf("%s: destructive %v", tool.Name, destructive)
		}
	}
	slices.Sort(names)
	want := []string{"browse_jellyfin", "create_backup", "create_jellyfin_channel", "create_youtube_channel", "delete_channel", "get_channel", "get_schedule",
		"list_antenna_channels", "list_backups", "list_channels", "lookup_youtube_channel", "search_youtube_channels", "set_channel_details",
		"update_jellyfin_channel", "update_youtube_channel"}
	if !slices.Equal(names, want) {
		t.Errorf("tools: %v", names)
	}
	if init := h.agent().InitializeResult(); init == nil || !strings.Contains(init.Instructions, "major.minor") {
		t.Errorf("instructions: %+v", init)
	}
}

func TestAgentJellyfinChannels(t *testing.T) {
	h := newHarness(t)
	h.admin.Antenna = func(context.Context) map[string]string {
		return map[string]string{"1.1": "WGBH", "1.3": "WBZ", "4.1": "WBZ"}
	}
	s := h.agent()

	// Before there's an account, browsing says so.
	fails(t, s, "browse_jellyfin", map[string]any{"kind": "series"}, "set up the Jellyfin account first")
	if code, _, raw := h.do("PUT", "/admin/api/account", map[string]string{"server": h.jf.URL, "user": "viewer", "password": "s3cret-pw"}); code != 200 {
		t.Fatalf("account: %s", raw)
	}

	st := ok(t, s, "list_channels", nil)
	if chans := list(st["channels"]); len(chans) != 1 || chans[0].(map[string]any)["plays"] != "0 video files" {
		t.Errorf("channels: %v", st["channels"])
	}
	if st["nextFree"] != "1.4" || st["antennaChannels"] != 3.0 || st["jellyfinAccount"].(map[string]any)["ok"] != true {
		t.Errorf("list_channels: %v", st)
	}
	if strings.Contains(fmt.Sprint(st), "s3cret") {
		t.Error("the password came back")
	}
	if ant := list(ok(t, s, "list_antenna_channels", nil)["antenna"]); len(ant) != 3 || ant[0].(map[string]any)["number"] != "1.1" {
		t.Errorf("antenna: %v", ant)
	}

	// Browsing, filtered and paged.
	b := ok(t, s, "browse_jellyfin", map[string]any{"kind": "series", "text": "FUTUR"})
	items := list(b["items"])
	if b["total"] != 2.0 || b["matched"] != 1.0 || len(items) != 1 || items[0].(map[string]any)["key"] != "Futurama (1999)" ||
		items[0].(map[string]any)["episodes"] != 2.0 || len(list(b["genres"])) != 2 {
		t.Errorf("browse futurama: %v", b)
	}
	if b := ok(t, s, "browse_jellyfin", map[string]any{"kind": "series", "genre": "game show"}); list(b["items"])[0].(map[string]any)["key"] != "Family Feud (1976)" {
		t.Errorf("by genre: %v", b)
	}
	if b := ok(t, s, "browse_jellyfin", map[string]any{"kind": "series", "yearTo": 1990}); b["matched"] != 1.0 {
		t.Errorf("by year: %v", b)
	}
	if b := ok(t, s, "browse_jellyfin", map[string]any{"kind": "series", "limit": 1}); len(list(b["items"])) != 1 || !strings.Contains(b["more"].(string), "offset 1") {
		t.Errorf("paged: %v", b)
	}
	fails(t, s, "browse_jellyfin", map[string]any{"kind": "music"}, "kind")

	// A new channel; a name that matches nothing is pointed out.
	c := ok(t, s, "create_jellyfin_channel", map[string]any{
		"number": "1.4", "name": "Cartoons", "series": []string{"Futurama (1999)", "Nonesuch"}, "callSign": "TOON", "category": "Kids",
	})
	ch := c["channel"].(map[string]any)
	if ch["kind"] != "jellyfin" || ch["folder"] != "1.4 Cartoons" || ch["callSign"] != "TOON" || !slices.Contains(list(ch["unmatched"]), "Nonesuch") ||
		!strings.Contains(fmt.Sprint(c["notes"]), "matched nothing") {
		t.Fatalf("create: %v", c)
	}
	fails(t, s, "create_jellyfin_channel", map[string]any{"number": "1.4", "name": "Again"}, "1.4 is taken by Cartoons")
	fails(t, s, "create_jellyfin_channel", map[string]any{"number": "one", "name": "x"}, "the number should look like 1.4")

	// Edits change only what's given.
	c = ok(t, s, "update_jellyfin_channel", map[string]any{
		"number": "1.4", "remove": map[string]any{"series": []string{"nonesuch"}}, "add": map[string]any{"movies": []string{"The Iron Giant"}}, "order": "aired",
	})
	cfg := c["channel"].(map[string]any)["config"].(map[string]any)
	if fmt.Sprint(cfg["series"]) != "[Futurama (1999)]" || fmt.Sprint(cfg["movies"]) != "[The Iron Giant]" || cfg["order"] != "aired" {
		t.Errorf("update: %v", cfg)
	}
	fails(t, s, "update_jellyfin_channel", map[string]any{"number": "1.4", "remove": map[string]any{"series": []string{"Seinfeld"}}}, `no series named "Seinfeld"`)
	fails(t, s, "update_jellyfin_channel", map[string]any{"number": "1.4"}, "nothing to change")
	fails(t, s, "update_jellyfin_channel", map[string]any{"number": "1.4", "shows": []string{"x"}}, "shows")
	fails(t, s, "update_youtube_channel", map[string]any{"number": "1.4", "maxHeight": 480}, "use update_jellyfin_channel")
	fails(t, s, "update_jellyfin_channel", map[string]any{"number": "1.2", "order": "aired"}, "only its details change here")

	g := ok(t, s, "get_channel", map[string]any{"number": "01.4"})
	if g["channel"].(map[string]any)["number"] != "1.4" || len(list(g["next"])) == 0 {
		t.Errorf("get_channel: %v", g)
	}
	if p := list(ok(t, s, "get_schedule", map[string]any{"number": "1.4"})["programs"]); len(p) == 0 {
		t.Error("no schedule")
	}
	fails(t, s, "get_channel", map[string]any{"number": "9.9"}, "there's no channel 9.9")

	// Details, a new number among them; the picks stay.
	c = ok(t, s, "set_channel_details", map[string]any{"number": "1.4", "newNumber": "1.5", "description": "Saturday mornings.", "enabled": false})
	ch = c["channel"].(map[string]any)
	if ch["number"] != "1.5" || ch["description"] != "Saturday mornings." || ch["enabled"] != false || ch["callSign"] != "TOON" {
		t.Errorf("details: %v", ch)
	}
	if raw, _ := os.ReadFile(filepath.Join(h.root, "1.5 Cartoons", channelFile)); !strings.Contains(string(raw), "Futurama (1999)") || !strings.Contains(string(raw), `"aired"`) {
		t.Errorf("jellyfin.json after details: %s", raw)
	}
	if c := ok(t, s, "set_channel_details", map[string]any{"number": "1.2", "callSign": "CATS"}); c["channel"].(map[string]any)["callSign"] != "CATS" {
		t.Errorf("folder details: %v", c)
	}
	fails(t, s, "set_channel_details", map[string]any{"number": "1.2", "category": "Cats"}, "category")
	fails(t, s, "set_channel_details", map[string]any{"number": "1.2", "callSign": "TOO LONG A SIGN"}, "the call sign")
	fails(t, s, "set_channel_details", map[string]any{"number": "1.2", "logo": "source"}, "only Jellyfin and YouTube channels have a picture")
	fails(t, s, "set_channel_details", map[string]any{"number": "1.2"}, "nothing to change")

	// Deleting takes a confirmation.
	fails(t, s, "delete_channel", map[string]any{"number": "1.5"}, "confirm")
	fails(t, s, "delete_channel", map[string]any{"number": "1.5", "confirm": false}, "can't be undone")
	fails(t, s, "delete_channel", map[string]any{"number": "1.2", "confirm": true}, "only Jellyfin and YouTube channels are deleted here")
	if d := ok(t, s, "delete_channel", map[string]any{"number": "1.5", "confirm": true}); d["deleted"].(map[string]any)["name"] != "Cartoons" {
		t.Errorf("delete: %v", d)
	}
	if _, err := os.Stat(filepath.Join(h.root, "1.5 Cartoons")); !os.IsNotExist(err) {
		t.Error("the deleted channel's folder is still there")
	}
}

func TestAgentYouTubeChannels(t *testing.T) {
	h := newHarness(t)
	s := h.agent()

	res := ok(t, s, "search_youtube_channels", map[string]any{"query": "boiler room"})
	found := list(res["channels"])
	if len(found) != 2 || found[0].(map[string]any)["handle"] != "@boilerroom" || found[0].(map[string]any)["image"] != nil {
		t.Errorf("search: %v", res)
	}
	fails(t, s, "search_youtube_channels", map[string]any{"query": "broken"}, "Sign in to confirm")
	if nl := ok(t, s, "lookup_youtube_channel", map[string]any{"channel": "@Northernlion"}); nl["videos"] != 20000.0 || nl["id"] != "UC3tNpTOHsTnkmbwztCs30sA" {
		t.Errorf("lookup: %v", nl)
	}
	fails(t, s, "lookup_youtube_channel", map[string]any{"channel": "https://youtu.be/dQw4w9WgXcQ"}, "link to a video")

	c := ok(t, s, "create_youtube_channel", map[string]any{
		"number": "1.9", "name": "DJ Sets", "channels": []string{"@boilerroom"}, "maxMinutes": 0, "rerunMix": "recent", "maxAgeDays": 90, "category": "Music",
	})
	if ch := c["channel"].(map[string]any); ch["kind"] != "youtube" || ch["category"] != "Music" || ch["youtube"].(map[string]any)["maxMinutes"] != 0.0 {
		t.Fatalf("create: %v", c)
	}
	h.settle("1.9")
	data, _ := os.ReadFile(filepath.Join(h.root, "1.9 DJ Sets", youtubeFile))
	if !strings.Contains(string(data), `"maxAgeDays": 90`) || !strings.Contains(string(data), `"rerunMix": "recent"`) || !strings.Contains(string(data), `"repeatDays": 30`) {
		t.Errorf("youtube.json: %s", data)
	}
	fails(t, s, "create_youtube_channel", map[string]any{"number": "1.10", "name": "x", "channels": []string{"@boilerroom"}, "maxHeight": 900}, "maxHeight")
	fails(t, s, "create_youtube_channel", map[string]any{"number": "1.10", "name": "x", "channels": []string{"https://youtu.be/dQw4w9WgXcQ"}}, "link to a video")
	fails(t, s, "create_youtube_channel", map[string]any{"number": "1.10", "name": "x", "channels": []string{"@a"}, "minMinutes": 20, "maxMinutes": 10}, "longer than the shortest")

	c = ok(t, s, "update_youtube_channel", map[string]any{"number": "1.9", "addChannels": []string{"youtube.com/@Northernlion"}, "maxHeight": 1080})
	yt := c["channel"].(map[string]any)["youtube"].(map[string]any)
	if fmt.Sprint(yt["channels"]) != "[https://www.youtube.com/@boilerroom https://www.youtube.com/@Northernlion]" || yt["maxHeight"] != 1080.0 || yt["rerunMix"] != "recent" {
		t.Errorf("add: %v", yt)
	}
	h.settle("1.9")
	c = ok(t, s, "update_youtube_channel", map[string]any{"number": "1.9", "removeChannels": []string{"boilerroom"}, "name": "Northernlion"})
	if ch := c["channel"].(map[string]any); ch["folder"] != "1.9 Northernlion" || len(list(ch["youtube"].(map[string]any)["channels"])) != 1 {
		t.Errorf("remove: %v", ch)
	}
	h.settle("1.9")
	fails(t, s, "update_youtube_channel", map[string]any{"number": "1.9", "removeChannels": []string{"@nobody"}}, `doesn't play "@nobody"`)
	fails(t, s, "update_youtube_channel", map[string]any{"number": "1.9", "rerunMix": "newest"}, "rerunMix")

	if l := list(ok(t, s, "list_channels", nil)["channels"]); len(l) != 2 || l[1].(map[string]any)["plays"] != "@Northernlion on YouTube" {
		t.Errorf("list: %v", l)
	}
	ok(t, s, "delete_channel", map[string]any{"number": "1.9", "confirm": true})
	if _, err := os.Stat(filepath.Join(h.root, "1.9 Northernlion")); !os.IsNotExist(err) {
		t.Error("the deleted channel's folder is still there")
	}
}

// TestAgentSchedule: schedules through the Jellyfin tools and
// set_channel_details, shown by list_channels and get_channel.
func TestAgentSchedule(t *testing.T) {
	h := newHarness(t)
	s := h.agent()
	if code, _, raw := h.do("PUT", "/admin/api/account", map[string]string{"server": h.jf.URL, "user": "viewer", "password": "s3cret-pw"}); code != 200 {
		t.Fatalf("account: %s", raw)
	}
	nightly := map[string]any{"start": "2026-10-04", "blocks": []any{map[string]any{"at": "20:00", "count": 4}}}
	c := ok(t, s, "create_jellyfin_channel", map[string]any{"number": "1.4", "name": "Futurama", "series": []string{"Futurama (1999)"}, "order": "aired",
		"schedule": nightly})
	if sch, _ := c["channel"].(map[string]any)["schedule"].(map[string]any); sch["start"] != "2026-10-04" {
		t.Fatalf("create: %v", c)
	}
	g := ok(t, s, "get_channel", map[string]any{"number": "1.4"})
	if items := list(g["items"]); len(items) != 2 || items[0] != "1. Futurama S1 E1: Space Pilot 3000 (22 min)" {
		t.Errorf("items: %v", g["items"])
	}
	if l := list(ok(t, s, "list_channels", nil)["channels"]); l[1].(map[string]any)["schedule"] == nil {
		t.Errorf("list: %v", l)
	}

	c = ok(t, s, "update_jellyfin_channel", map[string]any{"number": "1.4", "schedule": map[string]any{"start": "2026-10-04T21:00", "first": 2}})
	if sch, _ := c["channel"].(map[string]any)["schedule"].(map[string]any); sch["first"] != 2.0 || sch["blocks"] != nil {
		t.Errorf("update: %v", c)
	}
	c = ok(t, s, "update_jellyfin_channel", map[string]any{"number": "1.4", "order": "shuffle"})
	if c["channel"].(map[string]any)["schedule"] == nil {
		t.Errorf("left out, the schedule went: %v", c)
	}
	fails(t, s, "update_jellyfin_channel", map[string]any{"number": "1.4", "schedule": map[string]any{"start": "2026-10-04", "blocks": []any{map[string]any{"at": "20:00"}}}},
		"the schedule: block 1 needs a count or a time to stop")
	fails(t, s, "update_jellyfin_channel", map[string]any{"number": "1.4", "schedule": map[string]any{"start": "2026-10-04", "blocks": []any{map[string]any{"at": "20:00", "count": 0}}}},
		"count")
	if c := ok(t, s, "update_jellyfin_channel", map[string]any{"number": "1.4", "schedule": nil}); c["channel"].(map[string]any)["schedule"] != nil {
		t.Errorf("null: %v", c)
	}

	// A folder channel's, through set_channel_details.
	c = ok(t, s, "set_channel_details", map[string]any{"number": "1.2", "schedule": nightly})
	if c["channel"].(map[string]any)["schedule"] == nil {
		t.Errorf("folder: %v", c)
	}
	if g := ok(t, s, "get_channel", map[string]any{"number": "1.2"}); g["items"] == nil || len(list(g["items"])) != 0 {
		t.Errorf("an empty folder's items: %v", g["items"])
	}
	fails(t, s, "set_channel_details", map[string]any{"number": "1.2", "schedule": map[string]any{"start": "soon"}}, "the schedule: the start")
	if c := ok(t, s, "set_channel_details", map[string]any{"number": "1.2", "schedule": nil}); c["channel"].(map[string]any)["schedule"] != nil {
		t.Errorf("folder null: %v", c)
	}
}

// TestAgentEndpoint: the page's WebMCP calls (a plain JSON-RPC POST, no
// session), what's logged, and other sites' requests refused.
func TestAgentEndpoint(t *testing.T) {
	h := newHarness(t)
	var logged bytes.Buffer
	var mu sync.Mutex
	log.SetOutput(writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return logged.Write(p) }))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	post := func(body string, header map[string]string) (int, string) {
		req, _ := http.NewRequest("POST", h.url+mcpPath, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	code, raw := post(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_youtube_channels","arguments":{"query":"secret query"}}}`,
		map[string]string{viaHeader: "webmcp"})
	var res struct {
		Result mcp.CallToolResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &res); code != 200 || err != nil || res.Result.IsError || len(res.Result.Content) != 1 {
		t.Fatalf("page call: %d %s", code, raw)
	}
	if code, raw := post(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil); code != 200 || !strings.Contains(raw, `"delete_channel"`) {
		t.Errorf("tools/list: %d %.200s", code, raw)
	}
	mu.Lock()
	got := logged.String()
	mu.Unlock()
	if !strings.Contains(got, "admin: agent: search_youtube_channels (through the page)") || strings.Contains(got, "secret query") {
		t.Errorf("log: %q", got)
	}

	if code, raw := post(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://example.com"}); code != http.StatusForbidden {
		t.Errorf("another site's request: %d %s", code, raw)
	}
	resp, err := http.Get(h.url + mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", resp.StatusCode)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// TestAgentDemoServer serves the admin page and its agent endpoint with a
// fake Jellyfin server, a weather channel, a live YouTube channel and a
// few antenna channels, for trying agents and the page's WebMCP by hand:
//
//	AIRWAVES_ADMIN_DEMO=127.0.0.1:8790 go test ./internal/admin -run TestAgentDemoServer -timeout 0
//
// With AIRWAVES_YTDLP naming a yt-dlp, YouTube searches and channels are
// real; otherwise they come from testdata. It runs until interrupted.
func TestAgentDemoServer(t *testing.T) {
	addr := os.Getenv("AIRWAVES_ADMIN_DEMO")
	if addr == "" {
		t.Skip("set AIRWAVES_ADMIN_DEMO to an address to serve a demo admin page")
	}
	h := newHarness(t)
	if tool := os.Getenv("AIRWAVES_YTDLP"); tool != "" {
		h.lib.YtDlp = &vchan.YtDlp{Path: tool, HTTP: http.DefaultClient}
	}
	h.withWeather()
	h.admin.Antenna = func(context.Context) map[string]string {
		return map[string]string{"2.1": "WGBH", "4.1": "WBZ", "5.1": "WCVB", "7.1": "WHDH", "44.1": "WGBX"}
	}
	seedYouTube(t, filepath.Join(h.root, "1.8 Northernlion"))
	h.lib.Rescan()
	if code, _, raw := h.do("PUT", "/admin/api/account", map[string]string{"server": h.jf.URL, "user": "viewer", "password": "s3cret-pw"}); code != 200 {
		t.Fatalf("account: %s", raw)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h.admin.Handler()}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	fmt.Printf("demo admin page on http://%s/admin/, agents at http://%s%s, channels in %s\n", addr, addr, mcpPath, h.root)
	if err := srv.Serve(ln); err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
