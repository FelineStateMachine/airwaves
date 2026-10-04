package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAdminYouTubePlaylist: a playlist looked up, then played by a new
// channel, listed in order; switched to uploads and back; and refused
// along with channels, or as a video's link.
func TestAdminYouTubePlaylist(t *testing.T) {
	h := newHarness(t)
	const page = "https://www.youtube.com/playlist?list=PLshowtime0001"
	link := "https://www.youtube.com/watch?v=episode0002&list=PLshowtime0001&index=2"

	code, pl, raw := h.do("GET", "/admin/api/youtube/playlist?u="+url.QueryEscape(link), nil)
	if code != 200 || pl["id"] != "PLshowtime0001" || pl["url"] != page || pl["title"] != "The Show" || pl["owner"] != "Showrunner" ||
		pl["ownerUrl"] != "https://www.youtube.com/channel/UCshowsowner000000000001" || pl["videos"] != 13.0 ||
		pl["image"] != "https://i.ytimg.com/vi/episode0001/hqdefault.jpg" {
		t.Fatalf("lookup: %d %s", code, raw)
	}
	runs := h.ytRuns()
	if _, again, _ := h.do("GET", "/admin/api/youtube/playlist?u=PLshowtime0001", nil); again["title"] != "The Show" || h.ytRuns() != runs {
		t.Error("a repeat lookup ran yt-dlp again")
	}
	if code, _, raw := h.do("GET", "/admin/api/youtube/playlist?u=PLnosuch000000", nil); code != http.StatusBadGateway || !strings.Contains(raw, "The playlist does not exist.") {
		t.Errorf("no such playlist: %d %s", code, raw)
	}
	for u, want := range map[string]string{
		"https://youtu.be/dQw4w9WgXcQ":                                   "link to a video",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=RDdQw4w9WgXcQ": "mixes",
		"@boilerroom": "isn't a YouTube playlist",
	} {
		if code, _, raw := h.do("GET", "/admin/api/youtube/playlist?u="+url.QueryEscape(u), nil); code != http.StatusBadRequest || !strings.Contains(raw, want) {
			t.Errorf("lookup of %q: %d %s", u, code, raw)
		}
	}

	// A new channel plays it, from a start to come, two a night.
	code, ch, raw := h.do("PUT", "/admin/api/channels/new", map[string]any{
		"kind": "youtube", "number": "1.9", "name": "The Show",
		"youtube":  map[string]any{"playlist": link, "maxHeight": 480, "minMinutes": 20},
		"schedule": map[string]any{"start": "2099-01-01", "blocks": []any{map[string]any{"at": "20:00", "count": 2}}},
	})
	if code != 200 || ch["kind"] != "youtube" || ch["youtube"].(map[string]any)["playlist"] != page {
		t.Fatalf("create: %d %s", code, raw)
	}
	var file map[string]any
	data, _ := os.ReadFile(filepath.Join(h.root, "1.9 The Show", youtubeFile))
	if err := json.Unmarshal(data, &file); err != nil || file["playlist"] != page || file["channels"] != nil || file["maxHeight"] != 480.0 {
		t.Errorf("youtube.json: %s %v", data, err)
	}
	h.settle("1.9")
	show := h.channel("1.9")
	srcs, _ := show["sources"].([]any)
	if len(srcs) != 1 {
		t.Fatalf("sources: %v", show)
	}
	if s := srcs[0].(map[string]any); s["channel"] != page || s["url"] != page || s["id"] != "PLshowtime0001" || s["name"] != "The Show" ||
		s["owner"] != "Showrunner" || s["ownerUrl"] != "https://www.youtube.com/channel/UCshowsowner000000000001" ||
		s["image"] != "https://i.ytimg.com/vi/episode0001/hqdefault.jpg" || s["videos"] != 12.0 || s["playable"] != 12.0 ||
		s["listing"] != false || s["listed"] == nil {
		t.Errorf("source: %v", s)
	}
	if show["videos"] != 12.0 || show["items"] != 12.0 || show["listing"] != nil || show["schedule"] == nil {
		t.Errorf("channel: %v", show)
	}
	// Off the air until 2099, saying so, with its episodes in order.
	if now, _ := show["now"].(map[string]any); now == nil || now["title"] != "Off air" || !strings.Contains(now["subtitle"].(string), "with Episode 1") {
		t.Errorf("now: %v", show["now"])
	}
	_, items, raw := h.do("GET", "/admin/api/channels/1.9/items", nil)
	if list, _ := items["items"].([]any); len(list) != 12 || list[4].(map[string]any)["title"] != "Episode 5" ||
		list[4].(map[string]any)["subtitle"] != "Showrunner" || list[4].(map[string]any)["seconds"] != 3600.0 {
		t.Errorf("items: %s", raw)
	}

	// Both, or a playlist as a channel, are refused; a schedule for uploads too.
	for _, c := range []struct {
		yt   map[string]any
		more map[string]any
		want string
	}{
		{map[string]any{"playlist": page, "channels": []string{"@boilerroom"}}, nil, "not both"},
		{map[string]any{"playlist": "https://youtu.be/dQw4w9WgXcQ"}, nil, "link to a video"},
		{map[string]any{"playlist": "", "channels": []string{}}, nil, "at least one YouTube channel, or a playlist"},
		{map[string]any{"channels": []string{page}}, nil, "that's a playlist"},
		{map[string]any{"channels": []string{"@boilerroom"}}, map[string]any{"schedule": map[string]any{"start": "2099-01-01"}}, "uploads play in no order"},
	} {
		body := map[string]any{"kind": "youtube", "number": "1.9", "name": "The Show", "youtube": c.yt}
		for k, v := range c.more {
			body[k] = v
		}
		if code, _, raw := h.do("PUT", "/admin/api/channels/1.9", body); code != http.StatusBadRequest || !strings.Contains(raw, c.want) {
			t.Errorf("%v: %d %s", c.yt, code, raw)
		}
	}

	// Channels take the playlist's place, and it theirs.
	code, ch, raw = h.do("PUT", "/admin/api/channels/1.9", map[string]any{"kind": "youtube", "number": "1.9", "name": "The Show",
		"youtube": map[string]any{"channels": []string{"@boilerroom"}}})
	if cfg := ch["youtube"].(map[string]any); code != 200 || cfg["playlist"] != nil || len(cfg["channels"].([]any)) != 1 {
		t.Fatalf("to uploads: %d %s", code, raw)
	}
	h.settle("1.9")
	data, _ = os.ReadFile(filepath.Join(h.root, "1.9 The Show", youtubeFile))
	if strings.Contains(string(data), "playlist") || !strings.Contains(string(data), "@boilerroom") || !strings.Contains(string(data), `"minMinutes": 20`) {
		t.Errorf("youtube.json for uploads: %s", data)
	}
	code, ch, raw = h.do("PUT", "/admin/api/channels/1.9", map[string]any{"kind": "youtube", "number": "1.9", "name": "The Show",
		"youtube": map[string]any{"playlist": "PLshowtime0001"}})
	if cfg := ch["youtube"].(map[string]any); code != 200 || cfg["playlist"] != page || len(cfg["channels"].([]any)) != 0 {
		t.Fatalf("back to the playlist: %d %s", code, raw)
	}
	h.settle("1.9")
	if code, _, raw := h.do("DELETE", "/admin/api/channels/1.9", nil); code != http.StatusNoContent {
		t.Errorf("delete: %d %s", code, raw)
	}
}

func TestRestoreKnowsPlaylists(t *testing.T) {
	a := sourceOf(map[string][]byte{youtubeFile: []byte(`{"playlist": "https://www.youtube.com/watch?v=x&list=PLshowtime0001"}`)})
	b := sourceOf(map[string][]byte{youtubeFile: []byte(`{"playlist": "PLshowtime0001", "maxHeight": 480}`)})
	if a == "" || a != b || a == sourceOf(map[string][]byte{youtubeFile: []byte(`{"playlist": "PLSHOWTIME0001"}`)}) {
		t.Errorf("sources: %q %q", a, b)
	}
}

// TestAgentYouTubePlaylist: agents make and change a channel playing a
// playlist, and see it as one.
func TestAgentYouTubePlaylist(t *testing.T) {
	h := newHarness(t)
	s := h.agent()
	const page = "https://www.youtube.com/playlist?list=PLshowtime0001"

	c := ok(t, s, "create_youtube_channel", map[string]any{
		"number": "1.9", "name": "The Show", "playlist": "youtube.com/watch?v=episode0003&list=PLshowtime0001", "category": "Family",
		"schedule": map[string]any{"start": "2099-01-01", "blocks": []any{map[string]any{"at": "20:00", "count": 4}}},
	})
	if ch := c["channel"].(map[string]any); ch["youtube"].(map[string]any)["playlist"] != page || ch["category"] != "Family" || ch["schedule"] == nil {
		t.Fatalf("create: %v", c)
	}
	h.settle("1.9")
	g := ok(t, s, "get_channel", map[string]any{"number": "1.9"})
	if src := list(g["channel"].(map[string]any)["sources"]); len(src) != 1 || src[0].(map[string]any)["name"] != "The Show" {
		t.Errorf("get_channel's sources: %v", g["channel"])
	}
	if items := list(g["items"]); len(items) != 12 || items[2] != "3. Episode 3: Showrunner (60 min)" {
		t.Errorf("get_channel's items: %v", g["items"])
	}
	l := list(ok(t, s, "list_channels", nil)["channels"])
	if len(l) != 2 || l[1].(map[string]any)["plays"] != `the playlist "The Show" by Showrunner on YouTube, in order` {
		t.Errorf("list: %v", l)
	}
	fails(t, s, "create_youtube_channel", map[string]any{"number": "1.10", "name": "x", "playlist": page, "channels": []string{"@boilerroom"}}, "not both")
	fails(t, s, "create_youtube_channel", map[string]any{"number": "1.10", "name": "x"}, "at least one YouTube channel, or a playlist")
	fails(t, s, "update_youtube_channel", map[string]any{"number": "1.9", "playlist": "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=RDdQw4w9WgXcQ"}, "mixes")

	c = ok(t, s, "update_youtube_channel", map[string]any{"number": "1.9", "maxHeight": 1080, "schedule": map[string]any{"start": "2098-06-01"}})
	if ch := c["channel"].(map[string]any); ch["youtube"].(map[string]any)["maxHeight"] != 1080.0 || ch["schedule"].(map[string]any)["start"] != "2098-06-01" ||
		!strings.Contains(fmt.Sprint(c["notes"]), "listed again") {
		t.Errorf("update: %v", c)
	}
	h.settle("1.9")
	c = ok(t, s, "update_youtube_channel", map[string]any{"number": "1.9", "addChannels": []string{"@boilerroom"}})
	if yt := c["channel"].(map[string]any)["youtube"].(map[string]any); yt["playlist"] != nil || fmt.Sprint(yt["channels"]) != "[https://www.youtube.com/@boilerroom]" {
		t.Errorf("to uploads: %v", yt)
	}
	h.settle("1.9")
	fails(t, s, "update_youtube_channel", map[string]any{"number": "1.9", "schedule": map[string]any{"start": "2099-01-01"}}, "uploads play in no order")
	ok(t, s, "delete_channel", map[string]any{"number": "1.9", "confirm": true})
}
