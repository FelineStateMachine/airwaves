package admin

// Agents: the page's channel management as tools for AI agents, served
// over MCP (Streamable HTTP) at /admin/mcp. Each tool is a thin wrapper
// over the page's own API, run in-process through the same handler, so an
// agent gets the page's checks and behaviour exactly. The page registers
// the same tools with WebMCP (ui/webmcp.js), calling them here, so an
// agent working through the open page gets them too.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"airwaves/internal/guide"
	"airwaves/internal/vchan"
)

const (
	mcpPath = "/admin/mcp"
	// viaHeader marks the calls the page makes for WebMCP, for the log.
	viaHeader = "X-Airwaves-Via"
	// browseLimit is how many library items browse_jellyfin returns at
	// most, and browseDefault when the agent doesn't say.
	browseLimit   = 200
	browseDefault = 50
	// overviewCut is how much of an item's overview browse_jellyfin keeps.
	overviewCut = 200
)

// agentGuide is what an agent is told when it connects.
const agentGuide = `Airwaves is a home TV server: antenna channels from a tuner, and custom channels it makes itself, in one guide and in an emulated HDHomeRun lineup. These tools manage the custom channels, as the admin page at /admin/ does. Changes go live at once for every viewer, and there is no undo.

Every channel has a number "major.minor" (major 1 to 999, minor 0 to 999, like "1.4" or "104.0"; "1.05" and "1.5" are the same number), a short name (up to 48 characters, no slashes), and details: a call sign (up to 8 letters and digits, like TOON), a category, a description (up to 500 characters) and whether it is enabled (a disabled channel stays here but leaves the lineup and the app).

Numbers are unique among custom channels, the weather channel's included. A custom channel may take an antenna channel's number, but then replaces that channel in the HDHomeRun lineup, so pick a number list_antenna_channels doesn't show; list_channels suggests the next free one.

Kinds: weather (built in), folder (a folder of video files on the server, made by hand), jellyfin (series, movies, collections and playlists from the owner's Jellyfin server) and youtube (the uploads of up to 10 YouTube channels, or one YouTube playlist in its order, streamed, never downloaded). Jellyfin and YouTube channels are created, edited and deleted here; set_channel_details works on every kind.

Planning a channel:
1. list_channels and list_antenna_channels: what exists, and a free number.
2. YouTube: search_youtube_channels (by topic or name) or lookup_youtube_channel (by link or @handle), then create_youtube_channel with their @handles or links; or create_youtube_channel with a playlist's link, to play it in order. Jellyfin: browse_jellyfin for the exact keys ("Name (Year)"), then create_jellyfin_channel.
3. get_channel shows what was picked up (Jellyfin names that matched nothing, YouTube listing progress), and get_schedule what airs next.

Saving a channel's settings plans its schedule again from that moment, the current airing included.

Folder and Jellyfin channels, and YouTube channels playing a playlist, loop their videos in order around the clock. A schedule (the schedule input of set_channel_details and the Jellyfin and YouTube tools) makes one air in daily blocks from a start date instead, such as four episodes a night at 8 PM from the first, off the air between; get_channel lists the videos in the order they air, for picking the first.

The channel setup is backed up by itself 30 seconds after changes, and daily. Before a big reorganization, create_backup keeps a labelled copy until the owner deletes it; list_backups shows the backups. Restoring one is for a person, on the admin page.`

// addAgentRoutes serves the agent tools at /admin/mcp, each calling api,
// the page's API.
func addAgentRoutes(mux *http.ServeMux, api http.Handler) {
	h := agentHandler(api)
	// One pattern per method: "/admin/mcp" alone would clash with
	// "GET /admin/".
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		mux.Handle(m+" "+mcpPath, h)
	}
}

// agentHandler is the MCP endpoint. It is stateless, so a client keeps
// working across server restarts, and answers in plain JSON. Cross-origin
// browser requests are refused: only the page itself, and agents outside
// a browser, may call it.
func agentHandler(api http.Handler) http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "airwaves", Title: "Airwaves channels", Version: "1.0.0"},
		&mcp.ServerOptions{Instructions: agentGuide})
	(&agents{api: api}).register(srv)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.NewCrossOriginProtection().Handler(h)
}

// agents runs the tools against the page's API.
type agents struct {
	api http.Handler
}

// ---------- calling the API ----------

// recorder keeps an answer from the API.
type recorder struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.body.Write(p)
}

// call makes a request of the page's API, as the page would, and decodes
// the answer into out when it isn't nil. An error answer comes back as an
// error with the API's message.
func (a *agents) call(ctx context.Context, method, path string, body, out any) error {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := &recorder{header: http.Header{}}
	a.api.ServeHTTP(rec, req)
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	if rec.code >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(rec.body.Bytes(), &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s %s: %d %s", method, path, rec.code, strings.TrimSpace(rec.body.String()))
	}
	if out == nil || rec.code == http.StatusNoContent {
		return nil
	}
	return json.Unmarshal(rec.body.Bytes(), out)
}

// stateJSON is the page's state.
type stateJSON struct {
	Account    accountStatus `json:"account"`
	Channels   []channelJSON `json:"channels"`
	Categories []string      `json:"categories"`
	Antenna    []antennaJSON `json:"antenna"`
}

func (a *agents) state(ctx context.Context) (stateJSON, error) {
	var st stateJSON
	err := a.call(ctx, http.MethodGet, "/admin/api/state", nil, &st)
	return st, err
}

// channel finds the channel numbered number.
func (a *agents) channel(ctx context.Context, number string) (channelJSON, error) {
	st, err := a.state(ctx)
	if err != nil {
		return channelJSON{}, err
	}
	n := strings.TrimSpace(number)
	for _, c := range st.Channels {
		if vchan.SameNumber(c.Number, n) {
			return c, nil
		}
	}
	return channelJSON{}, fmt.Errorf("there's no channel %s; list_channels shows the channels there are", n)
}

func (a *agents) schedule(ctx context.Context, number string) ([]programJSON, error) {
	var s struct {
		Programs []programJSON `json:"programs"`
	}
	err := a.call(ctx, http.MethodGet, "/admin/api/channels/"+url.PathEscape(number)+"/schedule", nil, &s)
	return s.Programs, err
}

// put saves a channel as the page does: number is the one it has now, or
// "new".
func (a *agents) put(ctx context.Context, number string, body map[string]any) (channelJSON, error) {
	var c channelJSON
	err := a.call(ctx, http.MethodPut, "/admin/api/channels/"+url.PathEscape(number), body, &c)
	return c, err
}

// fresh reads a Jellyfin channel again once its first fetch from the
// server is done, which asking for its schedule waits for (up to 15
// seconds), so the agent sees what its picks matched. A save answers
// before then.
func (a *agents) fresh(ctx context.Context, c channelJSON) channelJSON {
	if c.Kind != "jellyfin" {
		return c
	}
	if _, err := a.schedule(ctx, c.Number); err != nil {
		return c
	}
	if f, err := a.channel(ctx, c.Number); err == nil {
		return f
	}
	return c
}

// ---------- schemas ----------

// object is an object schema. It refuses properties it doesn't name
// (additionalProperties false), so a misspelt one is an error rather than
// ignored.
func object(props map[string]*jsonschema.Schema, required ...string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", Properties: props, Required: required, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
}

func str(desc string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Description: desc}
}

func boolean(desc string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "boolean", Description: desc}
}

func strs(desc string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "array", Description: desc, Items: &jsonschema.Schema{Type: "string"}}
}

func num(desc string, lo, hi float64) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "number", Description: desc, Minimum: &lo, Maximum: &hi}
}

func integer(desc string, lo, hi float64) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "integer", Description: desc, Minimum: &lo, Maximum: &hi}
}

func enum[T any](typ, desc string, values []T) *jsonschema.Schema {
	s := &jsonschema.Schema{Type: typ, Description: desc}
	for _, v := range values {
		s.Enum = append(s.Enum, v)
	}
	return s
}

// with adds properties to a set.
func with(props map[string]*jsonschema.Schema, more ...map[string]*jsonschema.Schema) map[string]*jsonschema.Schema {
	for _, m := range more {
		for k, v := range m {
			props[k] = v
		}
	}
	return props
}

const (
	numberDesc    = `The channel's number, like "1.4".`
	newNumberDesc = `A new number for the channel, "major.minor" (major 1 to 999, minor 0 to 999); leave out to keep it.`
	nameDesc      = "The channel's name as guides show it: up to 48 characters, no slashes."
	newNameDesc   = "A new name, up to 48 characters, no slashes; leave out to keep it."
)

// detailProps are a channel's details, as every tool that saves them
// takes them.
func detailProps() map[string]*jsonschema.Schema {
	return map[string]*jsonschema.Schema{
		"callSign": str(`A short label guides show beside the number: up to 8 letters and digits, with & + ! . ' - and single spaces allowed ("TOON", "A&E"). "" removes it.`),
		"category": enum("string", "What guides file the channel under.", vchan.Categories),
		"description": str("A sentence or two about the channel for guides, up to 500 characters. " +
			`"" removes it.`),
		"enabled": boolean("false takes the channel out of the lineup and the app, keeping it here; true puts it back. New channels are enabled."),
	}
}

func ytSettingProps() map[string]*jsonschema.Schema {
	return map[string]*jsonschema.Schema{
		"repeatDays": num("No video airs again within this many days, while the channels have enough videos (then the one aired longest ago goes next). Default 30.", 0, 365),
		"maxHeight":  enum("integer", "The picture height streamed, at most. Default 720.", heights),
		"minMinutes": num("Videos shorter than this are left out (Shorts always are). Default 3.", 0, 600),
		"maxMinutes": num("Videos longer than this are left out; 0 for no limit. Must be more than minMinutes. Default 240, so raise it (or use 0) for long videos like ambience loops, livestream archives or full sets.", 0, 1440),
		"rerunMix": enum("string", "How reruns lean to recent uploads: recent picks about 70% of reruns from the last six months, 20% from the 18 months before and 10% from any time; balanced 40/30/30; any treats every upload alike. Default balanced.",
			rerunMixes),
		"maxAgeDays": num("Only videos uploaded within this many days play, first runs and reruns alike, aging out as days pass; 0 for any age. With none that recent, the channel leaves the lineup until a new upload arrives. Default 0.", 0, 3650),
		"deadAir":    boolean("true never repeats a video within repeatDays: a YouTube channel with nothing left to air sits out its turn, and when every video has aired, the channel shows \"Please stand by\" (\"Off air\" in the guide) until a new upload arrives. false (the default) cycles reruns instead, the one aired longest ago first."),
	}
}

// scheduleProp is a channel's schedule, as the tools that save one take
// it (vchan.Schedule).
func scheduleProp() *jsonschema.Schema {
	days := &jsonschema.Schema{Type: "array", Description: `The days it airs; leave out for every day.`,
		Items: enum("string", "A day.", []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"})}
	block := object(map[string]*jsonschema.Schema{
		"days":  days,
		"at":    str(`When it starts, "20:00" (24-hour, the server's time zone).`),
		"count": integer("How many videos it airs back to back. Give count or until.", 1, 100),
		"until": str(`When it stops, "23:30": nothing starts from then, and the video on finishes. Earlier than at is the next day's.`),
	}, "at")
	block.Description = "A daily run of the channel."
	most := 12
	s := object(map[string]*jsonschema.Schema{
		"start": str(`When the channel begins, in the server's time zone: "2026-10-04" (its first block that day) or "2026-10-04T20:00".`),
		"first": integer("The video it begins with, counting from 1 in the order get_channel's items lists. Default 1.", 1, 1e6),
		"blocks": {Type: "array", Description: "When it airs each day, up to 12 blocks; leave out to air around the clock from start.",
			Items: block, MaxItems: &most},
	}, "start")
	s.Type, s.Types = "", []string{"object", "null"}
	s.Description = "When a folder, Jellyfin or YouTube playlist channel airs (Jellyfin best with order aired): its videos in order from start, beginning with first and looping at the end; " +
		"with blocks only in them, off the air between, with the guide saying when it's back. " +
		`Four episodes a night at 8 PM from the first: {"start": "2026-10-04", "blocks": [{"at": "20:00", "count": 4}]}. ` +
		"null takes the schedule away, back to looping around the clock; left out, it stays as it is."
	return s
}

const ytChannelsDesc = `YouTube channels whose uploads play, up to 10: "@handle", a channel link (youtube.com/@handle, /channel/UC..., /c/name) or a "UC..." channel ID. Video links are refused, and a playlist goes in playlist.`

const ytPlaylistDesc = `A YouTube playlist to play instead of channels, in its order, looping at the end: its link (youtube.com/playlist?list=..., or a link to a video in it with list=) or its ID. ` +
	"Its private, deleted and upcoming videos are left out, and videos added to it join daily. Of the settings, only maxHeight applies to it. " +
	"It airs around the clock, or with a schedule only then, picking up where it left off."

func pickProps(verb string) map[string]*jsonschema.Schema {
	return map[string]*jsonschema.Schema{
		"series":      strs("Series " + verb + `, by browse_jellyfin's key ("Name (Year)") or plain name.`),
		"movies":      strs("Movies " + verb + ", likewise."),
		"collections": strs("Collections " + verb + ", by name."),
		"playlists":   strs("Playlists " + verb + ", by name."),
	}
}

func jellyfinSettingProps() map[string]*jsonschema.Schema {
	return map[string]*jsonschema.Schema{
		"order": enum("string", "shuffle (a fixed shuffle of everything picked) or aired (each series in season and episode order, movies by release date). Default shuffle.",
			[]string{"shuffle", "aired"}),
		"maxBitrate": num("A cap in Mb/s: the Jellyfin server transcodes to H.264 at that rate and at most 720p (4 is plenty for TV). 0 streams the original files. Default 0.", 0, 100),
	}
}

// ---------- tools ----------

var readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)}

// register adds the tools to srv.
func (a *agents) register(srv *mcp.Server) {
	addTool(srv, &mcp.Tool{
		Name:  "list_channels",
		Title: "List channels",
		Description: "Start here: every custom channel (weather, folder, Jellyfin and YouTube) with its number, name, kind, call sign, category, whether it's enabled, what it plays, " +
			"how many videos are in rotation and what's on now; the Jellyfin account's state; the categories; and nextFree, the lowest free 1.x number that no antenna channel uses.",
		InputSchema: object(map[string]*jsonschema.Schema{}),
		Annotations: readOnly,
	}, a.listChannels)

	addTool(srv, &mcp.Tool{
		Name:  "get_channel",
		Title: "Get a channel",
		Description: "One channel in full, as the admin page shows it: its details, its settings (config for Jellyfin: series, movies, collections, playlists, order, maxBitrate; " +
			"youtube for YouTube: channels and the settings), what it picked up (unmatched lists Jellyfin names that matched nothing; sources lists each YouTube channel's videos, " +
			"how many fit the settings, and whether it's still being listed), its schedule, any error, and the next airings; for a folder, Jellyfin or YouTube playlist channel, items lists its videos " +
			"in the order they air (numbered from 1, for a schedule's first).",
		InputSchema: object(map[string]*jsonschema.Schema{"number": str(numberDesc)}, "number"),
		Annotations: readOnly,
	}, a.getChannel)

	addTool(srv, &mcp.Tool{
		Name:  "get_schedule",
		Title: "Get a channel's schedule",
		Description: "What a channel airs next: up to 12 programs within the next 24 hours, with start and end times in the server's time zone. " +
			"Empty while a new YouTube channel's sources are being listed, or when nothing fits its settings or no Jellyfin pick matched.",
		InputSchema: object(map[string]*jsonschema.Schema{"number": str(numberDesc)}, "number"),
		Annotations: readOnly,
	}, a.getSchedule)

	addTool(srv, &mcp.Tool{
		Name:  "list_antenna_channels",
		Title: "List antenna channels",
		Description: "The antenna channels the tuner receives, by number. A custom channel with one of these numbers takes that channel's place in the HDHomeRun lineup, " +
			"so pick numbers not listed here.",
		InputSchema: object(map[string]*jsonschema.Schema{}),
		Annotations: readOnly,
	}, a.listAntenna)

	addTool(srv, &mcp.Tool{
		Name:  "browse_jellyfin",
		Title: "Browse the Jellyfin library",
		Description: "Search the Jellyfin server's series, movies, collections or playlists, for picks to put on a Jellyfin channel. Returns each match's key, the name to use in " +
			"create_jellyfin_channel and update_jellyfin_channel (\"Name (Year)\"), with year, genres, episode or item count, runtime and a short overview, a page at a time; " +
			"genres lists the most common genres of that kind, for the genre filter. Needs the Jellyfin account to work (see list_channels).",
		InputSchema: object(map[string]*jsonschema.Schema{
			"kind":     enum("string", "What to browse.", []string{"series", "movies", "collections", "playlists"}),
			"text":     str("Words that must all appear in the name or overview, ignoring case; name matches come first."),
			"genre":    str(`Only items with this genre, ignoring case ("Animation", "Comedy").`),
			"yearFrom": integer("Only items from this year on.", 1800, 2200),
			"yearTo":   integer("Only items up to this year.", 1800, 2200),
			"limit":    integer(fmt.Sprintf("How many items to return, default %d, at most %d.", browseDefault, browseLimit), 1, browseLimit),
			"offset":   integer("How many matches to skip, to page through them.", 0, 1e6),
		}, "kind"),
		Annotations: readOnly,
	}, a.browseJellyfin)

	addTool(srv, &mcp.Tool{
		Name:  "search_youtube_channels",
		Title: "Search YouTube channels",
		Description: "Find YouTube channels by name or topic (up to 15, with handle, link, ID, subscribers and description), for create_youtube_channel. " +
			"Runs yt-dlp on the server, so it takes several seconds; results are kept for 5 minutes.",
		InputSchema: object(map[string]*jsonschema.Schema{"query": str("A channel name or topic, up to 100 characters.")}, "query"),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(true)},
	}, a.searchYouTube)

	addTool(srv, &mcp.Tool{
		Name:  "lookup_youtube_channel",
		Title: "Look up a YouTube channel",
		Description: "One YouTube channel from its link, @handle or UC... ID: name, handle, link, ID, subscribers, roughly how many videos it has, and its description. " +
			"Runs yt-dlp on the server (up to a minute); results are kept for hours.",
		InputSchema: object(map[string]*jsonschema.Schema{"channel": str(`"@handle", a channel link or a "UC..." channel ID.`)}, "channel"),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(true)},
	}, a.lookupYouTube)

	addTool(srv, &mcp.Tool{
		Name:  "create_youtube_channel",
		Title: "Create a YouTube channel",
		Description: "Make a channel that runs the uploads of YouTube channels like a TV channel, with a real guide, or plays a YouTube playlist in order. Give channels or playlist. " +
			"Once saved, the server lists each source's uploads with yt-dlp (about 75 videos a second, so a 20,000-video channel takes about 5 minutes) and the channel joins " +
			"the lineup when all are listed; get_channel shows the progress. Settings left out take the defaults.",
		InputSchema: object(with(map[string]*jsonschema.Schema{
			"number":   str(`The new channel's number, "major.minor" (major 1 to 999, minor 0 to 999), like "1.9"; it must be free (see list_channels' nextFree).`),
			"name":     str(nameDesc),
			"channels": strs(ytChannelsDesc),
			"playlist": str(ytPlaylistDesc),
			"schedule": scheduleProp(),
		}, ytSettingProps(), detailProps()), "number", "name"),
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(true)},
	}, a.createYouTube)

	addTool(srv, &mcp.Tool{
		Name:  "update_youtube_channel",
		Title: "Update a YouTube channel",
		Description: "Change a YouTube channel's sources, playlist, settings, schedule, number or name; anything left out stays as it is. channels replaces the whole list, " +
			"addChannels and removeChannels change it; a playlist takes the channels' place, and channels the playlist's. Saving plans the schedule again from now, " +
			"the current airing included, and lists a playlist again. For call sign, category, description, enabled or logo use set_channel_details.",
		InputSchema: object(with(map[string]*jsonschema.Schema{
			"number":         str(numberDesc),
			"newNumber":      str(newNumberDesc),
			"name":           str(newNameDesc),
			"channels":       strs(ytChannelsDesc + " Replaces the list."),
			"addChannels":    strs("YouTube channels to add to the list, in the same forms."),
			"removeChannels": strs("YouTube channels to drop from the list: as listed, or by @handle, name or channel ID."),
			"playlist":       str(ytPlaylistDesc + " Replaces the channels, or the playlist it plays."),
			"schedule":       scheduleProp(),
		}, ytSettingProps()), "number"),
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(true)},
	}, a.updateYouTube)

	addTool(srv, &mcp.Tool{
		Name:  "create_jellyfin_channel",
		Title: "Create a Jellyfin channel",
		Description: "Make a channel that plays series, movies, collections and playlists from the owner's Jellyfin server on a fixed clock, with a full guide. " +
			"Use browse_jellyfin's keys; names match ignoring case, and a trailing year picks between same-named titles. The answer's unmatched lists names that matched nothing, " +
			"and items how many episodes and movies are in rotation. Needs the Jellyfin account to work.",
		InputSchema: object(with(map[string]*jsonschema.Schema{
			"number":   str(`The new channel's number, "major.minor" (major 1 to 999, minor 0 to 999), like "1.9"; it must be free (see list_channels' nextFree).`),
			"name":     str(nameDesc),
			"schedule": scheduleProp(),
		}, pickProps("to play"), jellyfinSettingProps(), detailProps()), "number", "name"),
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(false)},
	}, a.createJellyfin)

	pickSet := func(verb string) *jsonschema.Schema {
		s := object(pickProps(verb))
		s.Description = "Picks " + verb + "."
		return s
	}
	addTool(srv, &mcp.Tool{
		Name:  "update_jellyfin_channel",
		Title: "Update a Jellyfin channel",
		Description: "Change what a Jellyfin channel plays, its order, bitrate cap, schedule, number or name; anything left out stays as it is. series, movies, collections and " +
			"playlists replace those lists; add and remove change them. Adding or removing picks reshuffles the schedule. For call sign, category, description, enabled " +
			"or logo use set_channel_details.",
		InputSchema: object(with(map[string]*jsonschema.Schema{
			"number":    str(numberDesc),
			"newNumber": str(newNumberDesc),
			"name":      str(newNameDesc),
			"add":       pickSet("to add"),
			"remove":    pickSet("to remove, as listed or by plain name"),
			"schedule":  scheduleProp(),
		}, pickProps("to play, replacing the list"), jellyfinSettingProps()), "number"),
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(false)},
	}, a.updateJellyfin)

	addTool(srv, &mcp.Tool{
		Name:  "set_channel_details",
		Title: "Set a channel's details",
		Description: "Set any channel's details, the weather and folder channels' included: call sign, category, description, enabled, number, name and logo, " +
			"and a folder, Jellyfin or YouTube playlist channel's schedule. Anything left out stays as it is.",
		InputSchema: object(with(map[string]*jsonschema.Schema{
			"number":    str(numberDesc),
			"newNumber": str(newNumberDesc),
			"name":      str(newNameDesc),
			"schedule":  scheduleProp(),
			"logo": enum("string", "source makes the source's picture the logo (a Jellyfin channel's first pick's poster, collections first; a YouTube channel's first channel's avatar); "+
				"none removes the logo.", []string{"source", "none"}),
		}, detailProps()), "number"),
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(false)},
	}, a.setDetails)

	addTool(srv, &mcp.Tool{
		Name:  "delete_channel",
		Title: "Delete a channel",
		Description: "Delete a Jellyfin or YouTube channel: its folder's settings, details, logo, catalog and schedule go, and it leaves the lineup at once. " +
			"Can't be undone. Weather and folder channels aren't deleted here (set enabled false with set_channel_details to take one off the air).",
		InputSchema: object(map[string]*jsonschema.Schema{
			"number":  str(numberDesc),
			"confirm": boolean("Must be true: deleting can't be undone."),
		}, "number", "confirm"),
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(true), IdempotentHint: true, OpenWorldHint: new(false)},
	}, a.deleteChannel)

	addTool(srv, &mcp.Tool{
		Name:  "list_backups",
		Title: "List backups",
		Description: "The backups of the channel setup, newest first: each channel's settings, details, logo and YouTube schedule, the weather channel and the Jellyfin account " +
			"(never videos). For each, when and why it was made (change: 30 seconds after changes through the admin page or these tools; daily; restore: just before one; " +
			"manual: with create_backup or by the owner; uploaded), its label, and its channels. Restoring is for a person, on the admin page.",
		InputSchema: object(map[string]*jsonschema.Schema{}),
		Annotations: readOnly,
	}, a.listBackups)

	addTool(srv, &mcp.Tool{
		Name:  "create_backup",
		Title: "Back up the channel setup",
		Description: "Back up the channel setup now, with a label saying why, before a big change. Changes are backed up by themselves too, but those backups are pruned " +
			"(the last 30, and one a day for 14 days); one made here is kept until the owner deletes it. Changes nothing else.",
		InputSchema: object(map[string]*jsonschema.Schema{
			"label": str(fmt.Sprintf(`A short note on why, up to %d characters ("before reorganizing the kids channels").`, maxLabel)),
		}),
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(false), OpenWorldHint: new(false)},
	}, a.createBackup)
}

// addTool adds a tool whose run answers with a value the agent reads as
// JSON, or an error it reads as its message, and logs each call.
func addTool[In any](srv *mcp.Server, t *mcp.Tool, run func(context.Context, In) (any, error)) {
	mcp.AddTool(srv, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		out, err := run(ctx, in)
		logCall(req, t.Name, in, err, time.Since(start))
		if err != nil {
			return nil, nil, err
		}
		raw, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil, nil
	})
}

// numbered is a tool's input naming a channel.
type numbered interface{ channel() string }

// logCall notes a tool call: its name and channel, never what was asked
// or found.
func logCall(req *mcp.CallToolRequest, name string, in any, err error, took time.Duration) {
	what := name
	if n, ok := in.(numbered); ok && n.channel() != "" {
		what += " " + n.channel()
	}
	if req != nil && req.Extra != nil && req.Extra.Header.Get(viaHeader) == "webmcp" {
		what += " (through the page)"
	}
	switch {
	case err != nil:
		log.Printf("admin: agent: %s: %v", what, err)
	case took >= time.Millisecond:
		log.Printf("admin: agent: %s in %s", what, took.Round(time.Millisecond))
	default:
		log.Printf("admin: agent: %s", what)
	}
}

// ---------- inputs ----------

type noInput struct{}

type numberIn struct {
	Number string `json:"number"`
}

func (in numberIn) channel() string { return in.Number }

type detailsIn struct {
	CallSign    *string `json:"callSign"`
	Category    *string `json:"category"`
	Description *string `json:"description"`
	Enabled     *bool   `json:"enabled"`
}

func (d detailsIn) given() bool {
	return d.CallSign != nil || d.Category != nil || d.Description != nil || d.Enabled != nil
}

// into adds the details given to a channel as the page saves it.
func (d detailsIn) into(m map[string]any) {
	for k, v := range map[string]*string{"callSign": d.CallSign, "category": d.Category, "description": d.Description} {
		if v != nil {
			m[k] = *v
		}
	}
	if d.Enabled != nil {
		m["enabled"] = *d.Enabled
	}
}

// scheduleIn is a channel's schedule as the tools take it: left out, it
// stays as it is, and null takes it away.
type scheduleIn struct {
	Schedule json.RawMessage `json:"schedule"`
}

func (s scheduleIn) given() bool { return s.Schedule != nil }

// into adds the schedule given to a channel as the page saves it.
func (s scheduleIn) into(m map[string]any) {
	if s.Schedule != nil {
		m["schedule"] = s.Schedule
	}
}

type ytSettingsIn struct {
	RepeatDays *float64 `json:"repeatDays"`
	MaxHeight  *int     `json:"maxHeight"`
	MinMinutes *float64 `json:"minMinutes"`
	MaxMinutes *float64 `json:"maxMinutes"`
	RerunMix   *string  `json:"rerunMix"`
	MaxAgeDays *float64 `json:"maxAgeDays"`
	DeadAir    *bool    `json:"deadAir"`
}

func (y ytSettingsIn) given() bool {
	return y.RepeatDays != nil || y.MaxHeight != nil || y.MinMinutes != nil || y.MaxMinutes != nil || y.RerunMix != nil || y.MaxAgeDays != nil ||
		y.DeadAir != nil
}

// into adds the settings given to a YouTube channel's, as the page saves
// them.
func (y ytSettingsIn) into(m map[string]any) {
	for k, v := range map[string]*float64{"repeatDays": y.RepeatDays, "minMinutes": y.MinMinutes, "maxMinutes": y.MaxMinutes, "maxAgeDays": y.MaxAgeDays} {
		if v != nil {
			m[k] = *v
		}
	}
	if y.MaxHeight != nil {
		m["maxHeight"] = *y.MaxHeight
	}
	if y.RerunMix != nil {
		m["rerunMix"] = *y.RerunMix
	}
	if y.DeadAir != nil {
		m["deadAir"] = *y.DeadAir
	}
}

type picksIn struct {
	Series      []string `json:"series"`
	Movies      []string `json:"movies"`
	Collections []string `json:"collections"`
	Playlists   []string `json:"playlists"`
}

// lists pairs each of the picks with the kind it is and the list it
// changes.
func (p *picksIn) lists(c *channelConfig) []pickList {
	return []pickList{
		{"series", &p.Series, &c.Series}, {"movies", &p.Movies, &c.Movies},
		{"collections", &p.Collections, &c.Collections}, {"playlists", &p.Playlists, &c.Playlists},
	}
}

type pickList struct {
	kind      string
	given, to *[]string
}

type jellyfinSettingsIn struct {
	Order      *string  `json:"order"`
	MaxBitrate *float64 `json:"maxBitrate"`
}

// ---------- reading ----------

// channelLine is a channel as list_channels shows it.
type channelLine struct {
	Number     string `json:"number"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	CallSign   string `json:"callSign,omitempty"`
	Category   string `json:"category"`
	Enabled    bool   `json:"enabled"`
	Plays      string `json:"plays,omitempty"`
	InRotation *int   `json:"inRotation,omitempty"`
	Listing    bool   `json:"listing,omitempty"`
	OnNow      string `json:"onNow,omitempty"`
	Antenna    string `json:"replacesAntenna,omitempty"`
	// Schedule is when it airs, for one that has one.
	Schedule *vchan.Schedule `json:"schedule,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// plays says briefly what a channel plays.
func plays(c channelJSON) string {
	switch c.Kind {
	case "weather":
		return "local conditions, forecast and radar"
	case "folder":
		if c.Videos != nil {
			return count(*c.Videos, "video file", "video files")
		}
	case "jellyfin":
		if c.Config == nil {
			return ""
		}
		var parts []string
		for _, l := range []struct {
			list      []string
			one, many string
		}{
			{c.Config.Series, "series", "series"}, {c.Config.Movies, "movie", "movies"},
			{c.Config.Collections, "collection", "collections"}, {c.Config.Playlists, "playlist", "playlists"},
		} {
			if len(l.list) > 0 {
				parts = append(parts, count(len(l.list), l.one, l.many))
			}
		}
		if len(c.Unmatched) > 0 {
			parts = append(parts, count(len(c.Unmatched), "name that matched nothing", "names that matched nothing"))
		}
		return strings.Join(parts, ", ") + " from Jellyfin"
	case "youtube":
		var names []string
		for _, s := range c.Sources {
			if isPlaylist(c) {
				by := ""
				if s.Owner != "" {
					by = " by " + s.Owner
				}
				return fmt.Sprintf("the playlist %q%s on YouTube, in order", cmp(s.Name, s.URL), by)
			}
			names = append(names, cmp(s.Handle, cmp(s.Name, s.URL)))
		}
		return strings.Join(names, ", ") + " on YouTube"
	}
	return ""
}

// isPlaylist reports whether c is a YouTube channel playing a playlist.
func isPlaylist(c channelJSON) bool { return c.YouTube != nil && c.YouTube.Playlist != "" }

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func onNow(p *programJSON) string {
	if p == nil {
		return ""
	}
	if p.Subtitle != "" {
		return p.Title + ": " + p.Subtitle
	}
	return p.Title
}

// nextFree is the lowest 1.x number no channel, custom or antenna, uses.
func nextFree(st stateJSON) string {
	used := map[string]bool{}
	for _, c := range st.Channels {
		used[vchan.NumberKey(c.Number)] = true
	}
	for _, c := range st.Antenna {
		used[vchan.NumberKey(c.Number)] = true
	}
	for minor := 1; minor <= 999; minor++ {
		if n := fmt.Sprintf("1.%d", minor); !used[n] {
			return n
		}
	}
	return ""
}

func (a *agents) listChannels(ctx context.Context, _ noInput) (any, error) {
	st, err := a.state(ctx)
	if err != nil {
		return nil, err
	}
	lines := []channelLine{}
	for _, c := range st.Channels {
		l := channelLine{Number: c.Number, Name: c.Name, Kind: c.Kind, CallSign: c.CallSign, Category: c.Category, Enabled: c.Enabled,
			Plays: plays(c), Listing: c.Listing, OnNow: onNow(c.Now), Antenna: c.Antenna, Schedule: c.Schedule, Error: c.Error}
		if c.Kind == "jellyfin" || c.Kind == "youtube" {
			l.InRotation = c.Items
		}
		lines = append(lines, l)
	}
	slices.SortFunc(lines, func(x, y channelLine) int { return guide.CompareNumbers(x.Number, y.Number) })
	acct := map[string]any{"ok": st.Account.OK}
	for k, v := range map[string]string{"server": st.Account.Server, "user": st.Account.User, "serverName": st.Account.ServerName, "error": st.Account.Error} {
		if v != "" {
			acct[k] = v
		}
	}
	return map[string]any{
		"channels": lines, "jellyfinAccount": acct, "categories": st.Categories, "nextFree": nextFree(st),
		"antennaChannels": len(st.Antenna),
	}, nil
}

func (a *agents) getChannel(ctx context.Context, in numberIn) (any, error) {
	c, err := a.channel(ctx, in.Number)
	if err != nil {
		return nil, err
	}
	next, err := a.schedule(ctx, c.Number)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"channel": c, "next": next, "serverTime": time.Now().Format(time.RFC3339)}
	// A channel that plays in order lists its videos, for a schedule's first.
	var seq struct {
		Items []sequenceItemJSON `json:"items"`
	}
	if a.call(ctx, http.MethodGet, "/admin/api/channels/"+url.PathEscape(c.Number)+"/items", nil, &seq) == nil {
		items := []string{}
		for _, it := range seq.Items[:min(len(seq.Items), maxItemsListed)] {
			items = append(items, itemLine(it))
		}
		out["items"] = items
		if more := len(seq.Items) - len(items); more > 0 {
			out["moreItems"] = more
		}
	}
	return out, nil
}

// maxItemsListed is how many of a channel's videos get_channel lists.
const maxItemsListed = 500

// itemLine is a channel's video as get_channel lists it: "4. House of the
// Dragon S1 E4: The King of the Narrow Sea (58 min)".
func itemLine(it sequenceItemJSON) string {
	s := fmt.Sprintf("%d. %s", it.N, it.Title)
	if it.Season != "" && it.Episode != "" {
		s += fmt.Sprintf(" S%s E%s", it.Season, it.Episode)
	}
	if it.Subtitle != "" {
		s += ": " + it.Subtitle
	}
	return s + fmt.Sprintf(" (%d min)", (it.Seconds+30)/60)
}

func (a *agents) getSchedule(ctx context.Context, in numberIn) (any, error) {
	c, err := a.channel(ctx, in.Number)
	if err != nil {
		return nil, err
	}
	progs, err := a.schedule(ctx, c.Number)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"number": c.Number, "name": c.Name, "serverTime": time.Now().Format(time.RFC3339), "programs": progs}
	if len(progs) == 0 {
		out["note"] = emptyReason(c)
	}
	return out, nil
}

// emptyReason guesses why a channel has nothing to air.
func emptyReason(c channelJSON) string {
	switch {
	case c.Error != "":
		return "Nothing airs: " + c.Error
	case c.Listing && isPlaylist(c):
		return "Nothing airs yet: its playlist is still being listed; try again in a minute."
	case c.Listing:
		return "Nothing airs yet: its YouTube channels are still being listed; try again in a minute or two."
	case isPlaylist(c):
		return "Nothing airs: none of the playlist's videos can play (private, deleted, members-only, live and upcoming ones are left out)."
	case c.Kind == "youtube":
		return "Nothing airs: no video fits the settings (minMinutes, maxMinutes, maxAgeDays); get_channel shows how many each source has."
	case c.Kind == "jellyfin" && len(c.Unmatched) > 0:
		return "Nothing airs: some picks matched nothing on the Jellyfin server (" + strings.Join(c.Unmatched, ", ") + "); browse_jellyfin has the exact keys."
	case c.Kind == "jellyfin":
		return "Nothing airs: no episodes or movies to play; add picks with update_jellyfin_channel."
	}
	return "Nothing scheduled in the next 24 hours."
}

func (a *agents) listAntenna(ctx context.Context, _ noInput) (any, error) {
	st, err := a.state(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"antenna": st.Antenna}
	if len(st.Antenna) == 0 {
		out["note"] = "No antenna channels are known: the tuner may not be set up or scanned yet."
	}
	return out, nil
}

type browseIn struct {
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Genre    string `json:"genre"`
	YearFrom int    `json:"yearFrom"`
	YearTo   int    `json:"yearTo"`
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
}

// libraryItem is a Jellyfin item as browse_jellyfin shows it.
type libraryItem struct {
	Key            string   `json:"key"`
	Year           int      `json:"year,omitempty"`
	Genres         []string `json:"genres,omitempty"`
	Episodes       int      `json:"episodes,omitempty"`
	Items          int      `json:"items,omitempty"`
	RuntimeMinutes int      `json:"runtimeMinutes,omitempty"`
	Overview       string   `json:"overview,omitempty"`
}

type genreCount struct {
	Genre string `json:"genre"`
	Count int    `json:"count"`
}

func (a *agents) browseJellyfin(ctx context.Context, in browseIn) (any, error) {
	var lib struct {
		Items []itemJSON `json:"items"`
	}
	if err := a.call(ctx, http.MethodGet, "/admin/api/library/"+url.PathEscape(in.Kind), nil, &lib); err != nil {
		return nil, err
	}
	words := strings.Fields(strings.ToLower(in.Text))
	all := func(s string) bool {
		s = strings.ToLower(s)
		for _, w := range words {
			if !strings.Contains(s, w) {
				return false
			}
		}
		return true
	}
	genres := map[string]int{}
	var byName, byOverview []itemJSON
	for _, it := range lib.Items {
		for _, g := range it.Genres {
			genres[g]++
		}
		switch {
		case in.Genre != "" && !slices.ContainsFunc(it.Genres, func(g string) bool { return strings.EqualFold(g, strings.TrimSpace(in.Genre)) }):
		case in.YearFrom > 0 && it.Year < in.YearFrom, in.YearTo > 0 && (it.Year == 0 || it.Year > in.YearTo):
		case all(it.Key):
			byName = append(byName, it)
		case all(it.Key + " " + it.Overview):
			byOverview = append(byOverview, it)
		}
	}
	matched := append(byName, byOverview...)
	limit := in.Limit
	if limit == 0 {
		limit = browseDefault
	}
	from := min(in.Offset, len(matched))
	page := matched[from:min(from+limit, len(matched))]
	items := []libraryItem{}
	for _, it := range page {
		x := libraryItem{Key: it.Key, Year: it.Year, Genres: it.Genres, RuntimeMinutes: it.RuntimeMinutes, Overview: cut(it.Overview, overviewCut)}
		if in.Kind == "series" {
			x.Episodes = it.Count
		} else {
			x.Items = it.Count
		}
		items = append(items, x)
	}
	top := []genreCount{}
	for g, n := range genres {
		top = append(top, genreCount{g, n})
	}
	slices.SortFunc(top, func(x, y genreCount) int {
		if x.Count != y.Count {
			return y.Count - x.Count
		}
		return strings.Compare(x.Genre, y.Genre)
	})
	out := map[string]any{"kind": in.Kind, "total": len(lib.Items), "matched": len(matched), "offset": from, "items": items, "genres": top[:min(len(top), 25)]}
	if more := len(matched) - from - len(page); more > 0 {
		out["more"] = fmt.Sprintf("%d more; ask again with offset %d", more, from+len(page))
	}
	return out, nil
}

// cut shortens s to about n characters, at a word.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	if i := strings.LastIndexByte(string(r), ' '); i > n/2 {
		return string(r)[:i] + "..."
	}
	return string(r) + "..."
}

// ytFound is a YouTube channel a search or lookup found, as agents see it.
type ytFound struct {
	Name        string `json:"name"`
	Handle      string `json:"handle,omitempty"`
	URL         string `json:"url"`
	ID          string `json:"id"`
	Subscribers int64  `json:"subscribers"`
	Videos      int    `json:"videos,omitempty"`
	Description string `json:"description,omitempty"`
}

func found(c ytChannelJSON) ytFound {
	return ytFound{Name: c.Name, Handle: c.Handle, URL: c.URL, ID: c.ID, Subscribers: c.Subscribers, Videos: c.Videos, Description: c.Description}
}

type searchIn struct {
	Query string `json:"query"`
}

func (a *agents) searchYouTube(ctx context.Context, in searchIn) (any, error) {
	var res struct {
		Channels []ytChannelJSON `json:"channels"`
	}
	if err := a.call(ctx, http.MethodGet, "/admin/api/youtube/search?q="+url.QueryEscape(in.Query), nil, &res); err != nil {
		return nil, err
	}
	out := []ytFound{}
	for _, c := range res.Channels {
		out = append(out, found(c))
	}
	return map[string]any{"query": in.Query, "channels": out}, nil
}

type lookupIn struct {
	Channel string `json:"channel"`
}

func (a *agents) lookupYouTube(ctx context.Context, in lookupIn) (any, error) {
	var c ytChannelJSON
	if err := a.call(ctx, http.MethodGet, "/admin/api/youtube/channel?u="+url.QueryEscape(in.Channel), nil, &c); err != nil {
		return nil, err
	}
	return found(c), nil
}

// ---------- saving ----------

// saved is a channel just saved, with what an agent should know about it.
func saved(c channelJSON, notes ...string) map[string]any {
	if c.Antenna != "" {
		notes = append(notes, fmt.Sprintf("%s takes the place of antenna channel %s %s in the HDHomeRun lineup; if that isn't meant, move it to a free number with newNumber.",
			c.Number, c.Number, c.Antenna))
	}
	if len(c.Unmatched) > 0 {
		notes = append(notes, "These picks matched nothing on the Jellyfin server: "+strings.Join(c.Unmatched, ", ")+"; browse_jellyfin has the exact keys.")
	}
	switch {
	case c.Listing && isPlaylist(c):
		notes = append(notes, "Its playlist is being listed now; it joins the lineup once it is. get_channel shows the progress.")
	case c.Listing:
		notes = append(notes, "Its YouTube channels are being listed now (about 75 videos a second); it joins the lineup once they all are. get_channel shows the progress.")
	}
	if c.Error != "" {
		notes = append(notes, "Error: "+c.Error)
	}
	out := map[string]any{"channel": c}
	if len(notes) > 0 {
		out["notes"] = notes
	}
	return out
}

type createYouTubeIn struct {
	Number   string   `json:"number"`
	Name     string   `json:"name"`
	Channels []string `json:"channels"`
	Playlist string   `json:"playlist"`
	ytSettingsIn
	detailsIn
	scheduleIn
}

func (in createYouTubeIn) channel() string { return in.Number }

func (a *agents) createYouTube(ctx context.Context, in createYouTubeIn) (any, error) {
	yt := map[string]any{"channels": in.Channels, "playlist": in.Playlist}
	in.ytSettingsIn.into(yt)
	body := map[string]any{"kind": "youtube", "number": in.Number, "name": in.Name, "youtube": yt}
	in.detailsIn.into(body)
	in.scheduleIn.into(body)
	c, err := a.put(ctx, "new", body)
	if err != nil {
		return nil, err
	}
	return saved(c), nil
}

type updateYouTubeIn struct {
	Number         string   `json:"number"`
	NewNumber      string   `json:"newNumber"`
	Name           string   `json:"name"`
	Channels       []string `json:"channels"`
	AddChannels    []string `json:"addChannels"`
	RemoveChannels []string `json:"removeChannels"`
	Playlist       *string  `json:"playlist"`
	ytSettingsIn
	scheduleIn
}

func (in updateYouTubeIn) channel() string { return in.Number }

func (a *agents) updateYouTube(ctx context.Context, in updateYouTubeIn) (any, error) {
	cur, err := a.channel(ctx, in.Number)
	if err != nil {
		return nil, err
	}
	if cur.Kind != "youtube" {
		return nil, wrongKind(cur, "youtube")
	}
	if in.NewNumber == "" && in.Name == "" && in.Channels == nil && in.AddChannels == nil && in.RemoveChannels == nil && in.Playlist == nil &&
		!in.ytSettingsIn.given() && !in.scheduleIn.given() {
		return nil, errors.New("nothing to change: pass channels, addChannels, removeChannels, playlist, a setting, schedule, newNumber or name")
	}
	yt := map[string]any{}
	in.ytSettingsIn.into(yt)
	if in.Playlist != nil {
		yt["playlist"] = *in.Playlist
	}
	if in.Channels != nil || in.AddChannels != nil || in.RemoveChannels != nil {
		list := in.Channels
		if list == nil && cur.YouTube != nil {
			list = slices.Clone(cur.YouTube.Channels)
		}
		for _, r := range in.RemoveChannels {
			i := slices.IndexFunc(list, func(ch string) bool { return sameSource(ch, r, cur.Sources) })
			if i < 0 {
				return nil, fmt.Errorf("%s doesn't play %q; it plays %s", cur.Number, r, strings.Join(list, ", "))
			}
			list = slices.Delete(list, i, i+1)
		}
		yt["channels"] = append(list, in.AddChannels...)
	}
	body := map[string]any{"kind": "youtube", "number": cmp(in.NewNumber, cur.Number), "name": cmp(in.Name, cur.Name), "youtube": yt}
	in.scheduleIn.into(body)
	c, err := a.put(ctx, cur.Number, body)
	if err != nil {
		return nil, err
	}
	if isPlaylist(c) {
		return saved(c, "The playlist is listed again, for videos added to it."), nil
	}
	return saved(c, "The schedule was planned again from now."), nil
}

// sameSource reports whether ch, a YouTube channel in a channel's list,
// is the one given: the same page, or a source's handle, name or ID.
func sameSource(ch, given string, sources []ytSourceJSON) bool {
	given = strings.TrimSpace(given)
	page, err := youtubeChannel(ch)
	if err != nil {
		page = ch
	}
	if p, err := youtubeChannel(given); err == nil && strings.EqualFold(p, page) {
		return true
	}
	for _, s := range sources {
		if strings.EqualFold(s.Channel, ch) || strings.EqualFold(s.URL, page) {
			for _, v := range []string{s.Handle, s.Name, s.ID, strings.TrimPrefix(s.Handle, "@")} {
				if v != "" && strings.EqualFold(v, given) {
					return true
				}
			}
		}
	}
	return strings.EqualFold(ch, given)
}

func wrongKind(c channelJSON, want string) error {
	if c.Kind == "jellyfin" || c.Kind == "youtube" {
		return fmt.Errorf("%s %s is a %s channel: use update_%s_channel, or set_channel_details for its details", c.Number, c.Name, kindLabel[c.Kind], c.Kind)
	}
	return fmt.Errorf("%s %s is a %s channel, not a %s one: only its details change here, with set_channel_details", c.Number, c.Name, kindLabel[c.Kind], kindLabel[want])
}

type createJellyfinIn struct {
	Number string `json:"number"`
	Name   string `json:"name"`
	picksIn
	jellyfinSettingsIn
	detailsIn
	scheduleIn
}

func (in createJellyfinIn) channel() string { return in.Number }

func (a *agents) createJellyfin(ctx context.Context, in createJellyfinIn) (any, error) {
	body := map[string]any{"kind": "jellyfin", "number": in.Number, "name": in.Name}
	jellyfinBody(body, channelConfig{Series: in.Series, Movies: in.Movies, Collections: in.Collections, Playlists: in.Playlists,
		Order: deref(in.Order), MaxBitrate: deref(in.MaxBitrate)})
	in.detailsIn.into(body)
	in.scheduleIn.into(body)
	c, err := a.put(ctx, "new", body)
	if err != nil {
		return nil, err
	}
	return saved(a.fresh(ctx, c)), nil
}

// jellyfinBody adds a Jellyfin channel's whole config to a channel as the
// page saves it: whatever it leaves out is removed.
func jellyfinBody(m map[string]any, c channelConfig) {
	m["series"], m["movies"], m["collections"], m["playlists"] = orEmpty(c.Series), orEmpty(c.Movies), orEmpty(c.Collections), orEmpty(c.Playlists)
	m["order"], m["maxBitrate"] = cmp(c.Order, "shuffle"), c.MaxBitrate
}

func orEmpty(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

type updateJellyfinIn struct {
	Number    string   `json:"number"`
	NewNumber string   `json:"newNumber"`
	Name      string   `json:"name"`
	Add       *picksIn `json:"add"`
	Remove    *picksIn `json:"remove"`
	picksIn
	jellyfinSettingsIn
	scheduleIn
}

func (in updateJellyfinIn) channel() string { return in.Number }

func (a *agents) updateJellyfin(ctx context.Context, in updateJellyfinIn) (any, error) {
	cur, err := a.channel(ctx, in.Number)
	if err != nil {
		return nil, err
	}
	if cur.Kind != "jellyfin" {
		return nil, wrongKind(cur, "jellyfin")
	}
	cfg := channelConfig{Order: "shuffle"}
	if cur.Config != nil {
		cfg = *cur.Config
	}
	changed := in.NewNumber != "" || in.Name != "" || in.Order != nil || in.MaxBitrate != nil || in.scheduleIn.given()
	for _, l := range in.picksIn.lists(&cfg) {
		if *l.given != nil {
			*l.to, changed = *l.given, true
		}
	}
	if in.Remove != nil {
		for _, l := range in.Remove.lists(&cfg) {
			for _, r := range *l.given {
				i := slices.IndexFunc(*l.to, func(p string) bool { return samePick(p, r) })
				if i < 0 {
					return nil, fmt.Errorf("%s has no %s named %q; its %s: %s", cur.Number, l.kind, r, l.kind, listOrNone(*l.to))
				}
				*l.to, changed = slices.Delete(slices.Clone(*l.to), i, i+1), true
			}
		}
	}
	if in.Add != nil {
		for _, l := range in.Add.lists(&cfg) {
			for _, p := range *l.given {
				if !slices.ContainsFunc(*l.to, func(q string) bool { return strings.EqualFold(strings.TrimSpace(q), strings.TrimSpace(p)) }) {
					*l.to, changed = append(slices.Clone(*l.to), p), true
				}
			}
		}
	}
	if !changed {
		return nil, errors.New("nothing to change: pass picks (series, movies, collections, playlists, add or remove), order, maxBitrate, schedule, newNumber or name")
	}
	if in.Order != nil {
		cfg.Order = *in.Order
	}
	if in.MaxBitrate != nil {
		cfg.MaxBitrate = *in.MaxBitrate
	}
	body := map[string]any{"kind": "jellyfin", "number": cmp(in.NewNumber, cur.Number), "name": cmp(in.Name, cur.Name)}
	jellyfinBody(body, cfg)
	in.scheduleIn.into(body)
	c, err := a.put(ctx, cur.Number, body)
	if err != nil {
		return nil, err
	}
	return saved(a.fresh(ctx, c)), nil
}

// samePick reports whether p, a pick in a channel's config, is the one
// given: the same, ignoring case, or the same but for p's year.
func samePick(p, given string) bool {
	p, given = strings.TrimSpace(p), strings.TrimSpace(given)
	if strings.EqualFold(p, given) {
		return true
	}
	if i := strings.LastIndex(p, " ("); i > 0 && strings.HasSuffix(p, ")") {
		return strings.EqualFold(p[:i], given)
	}
	return false
}

func listOrNone(l []string) string {
	if len(l) == 0 {
		return "none"
	}
	return strings.Join(l, ", ")
}

type setDetailsIn struct {
	Number    string `json:"number"`
	NewNumber string `json:"newNumber"`
	Name      string `json:"name"`
	Logo      string `json:"logo"`
	detailsIn
	scheduleIn
}

func (in setDetailsIn) channel() string { return in.Number }

func (a *agents) setDetails(ctx context.Context, in setDetailsIn) (any, error) {
	cur, err := a.channel(ctx, in.Number)
	if err != nil {
		return nil, err
	}
	resave := in.NewNumber != "" || in.Name != "" || in.detailsIn.given() || in.scheduleIn.given()
	if !resave && in.Logo == "" {
		return nil, errors.New("nothing to change: pass callSign, category, description, enabled, logo, schedule, newNumber or name")
	}
	c := cur
	if resave {
		body := map[string]any{"kind": cur.Kind, "number": cmp(in.NewNumber, cur.Number), "name": cmp(in.Name, cur.Name)}
		if cur.Kind == "jellyfin" {
			// The page saves a Jellyfin channel's config with its details:
			// send it as it is.
			cfg := channelConfig{}
			if cur.Config != nil {
				cfg = *cur.Config
			}
			jellyfinBody(body, cfg)
		}
		in.detailsIn.into(body)
		in.scheduleIn.into(body)
		if c, err = a.put(ctx, cur.Number, body); err != nil {
			return nil, err
		}
	}
	logo := "/admin/api/channels/" + url.PathEscape(c.Number) + "/logo"
	switch in.Logo {
	case "source":
		err = a.call(ctx, http.MethodPost, logo+"/source", nil, &c)
	case "none":
		err = a.call(ctx, http.MethodDelete, logo, nil, &c)
	}
	if err != nil {
		return nil, fmt.Errorf("the logo: %w", err)
	}
	return saved(a.fresh(ctx, c)), nil
}

type deleteIn struct {
	Number  string `json:"number"`
	Confirm bool   `json:"confirm"`
}

func (in deleteIn) channel() string { return in.Number }

func (a *agents) deleteChannel(ctx context.Context, in deleteIn) (any, error) {
	cur, err := a.channel(ctx, in.Number)
	if err != nil {
		return nil, err
	}
	if !in.Confirm {
		return nil, fmt.Errorf("pass confirm: true to delete %s %s; it can't be undone", cur.Number, cur.Name)
	}
	if err := a.call(ctx, http.MethodDelete, "/admin/api/channels/"+url.PathEscape(cur.Number), nil, nil); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": map[string]string{"number": cur.Number, "name": cur.Name, "kind": cur.Kind}}, nil
}

// ---------- backups ----------

// backupLine is a backup as list_backups shows it.
type backupLine struct {
	File      string   `json:"file"`
	Created   string   `json:"created"`
	Reason    string   `json:"reason"`
	Label     string   `json:"label,omitempty"`
	Kept      string   `json:"kept"`
	Channels  []string `json:"channels"`
	Weather   bool     `json:"weather,omitempty"`
	Account   bool     `json:"jellyfinAccount,omitempty"`
	Schedules bool     `json:"youtubeSchedules,omitempty"`
	Error     string   `json:"error,omitempty"`
}

func lineOf(b backupJSON) backupLine {
	l := backupLine{File: b.File, Created: b.Created.Local().Format(time.RFC3339), Reason: b.Reason, Label: b.Label, Kept: "until deleted",
		Channels: []string{}, Weather: b.Weather, Account: b.Account, Schedules: b.Schedules, Error: b.Error}
	if b.Auto {
		l.Kept = "pruned in time"
	}
	for _, f := range b.Folders {
		l.Channels = append(l.Channels, f.Folder+" ("+kindLabel[f.Kind]+")")
	}
	return l
}

type listBackupsIn struct{}

func (a *agents) listBackups(ctx context.Context, _ listBackupsIn) (any, error) {
	var res struct {
		Backups []backupJSON `json:"backups"`
		Pending bool         `json:"pending"`
	}
	if err := a.call(ctx, http.MethodGet, "/admin/api/backups", nil, &res); err != nil {
		return nil, err
	}
	lines := []backupLine{}
	for _, b := range res.Backups {
		lines = append(lines, lineOf(b))
	}
	out := map[string]any{"backups": lines, "serverTime": time.Now().Format(time.RFC3339)}
	if res.Pending {
		out["note"] = "Changes made in the last 30 seconds aren't in a backup yet; they will be shortly."
	}
	return out, nil
}

type createBackupIn struct {
	Label string `json:"label"`
}

func (a *agents) createBackup(ctx context.Context, in createBackupIn) (any, error) {
	var b backupJSON
	if err := a.call(ctx, http.MethodPost, "/admin/api/backups", map[string]string{"label": in.Label}, &b); err != nil {
		return nil, err
	}
	return map[string]any{"backup": lineOf(b)}, nil
}
