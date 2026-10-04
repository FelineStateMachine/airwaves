# Airwaves

A desktop TV and program guide for free over-the-air broadcasts, built with Go and
[Wails v2](https://wails.io). It answers three questions for a US ZIP code:

1. **What is on the air here?** Every licensed transmitter within a radius, from the FCC.
2. **What can I actually receive?** A terrain-aware signal estimate per transmitter for
   indoor, attic and rooftop antennas, with a radar map, aiming advice and a path profile.
3. **What is on now?** A Gracenote-backed grid guide, live TV, and recordings.

There is one setup: an HDHomeRun on the network as the tuner, a home server running
`airwavesd` and Tvheadend, and the desktop app as the TV. The server owns the lineup,
guide, recording rules and library; the app is the remote.

It also maps which stations broadcast ATSC 3.0 (NextGen TV), which transmitter hosts each
service, and where the ATSC 1.0 simulcast of each channel lives.

## Run

Requirements: Go 1.26+ and the Wails CLI. On first launch the app asks for the server
address (for example `nas.local`).

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0
wails build                      # produces build/bin/Airwaves.app
open build/bin/Airwaves.app
wails dev                        # live-reload development
```

There is also a terminal report that needs only Go:

```sh
go run ./cmd/otascan -zip 80302                 # rooftop antenna, receivable channels
go run ./cmd/otascan -zip 80302 -antenna indoor
go run ./cmd/otascan -zip 80302 -lat 39.74 -lon -104.99 -all -json > report.json
```

## Server

The server is three containers: Tvheadend (tuners, scanning, writing recordings),
`airwavesd` (lineup, reception, guide, recording rules, live and recorded HLS, the
emulated HDHomeRun and custom channels) and the WeatherStar display behind the weather
channel.

```sh
AIRWAVES_ZIP=80302 scripts/deploy.sh nas   # cross-compiles airwavesd, syncs deploy/ to ~/airwaves, starts it
```

`deploy.sh` writes `~/airwaves/.env` on first run with the host name, its Tailscale
address, the host's time zone (`TZ`) and the antenna's ZIP code (`AIRWAVES_ZIP`, which
sets the lineup and listings), and binds `airwavesd` (port 8089) to the Tailscale address
only. Tvheadend listens on 127.0.0.1 only; reach its admin UI with
`ssh -L 9981:127.0.0.1:9981 nas`, then http://localhost:9981. Other `.env` settings:
`AIRWAVES_LAT` / `AIRWAVES_LON` for the
antenna's exact position (better than the ZIP centroid, since terrain decides
reception); `AIRWAVES_TOKEN` (requires the same token in the app); `RECORDINGS` (host
path for recordings, default `~/airwaves/recordings`); `MUSIC` and `CHANNELS` (host
folders for the custom channels); `TZ`. Location is deployment
configuration only; the app never shows or edits it.

In the app, open Settings and set **Airwaves server** to the server's name, `nas`
in these examples.

**The TV in a browser**: `airwavesd` also serves the app itself at `/tv/`, for example
`http://nas:8089/tv/`, to any browser on the tailnet and to the Android TV app, which
shows that page. It is the same interface over the same API (`frontend/dist/web.js`
stands in for the desktop app's Go side), so `AIRWAVES_TOKEN` still applies: the page
asks for the token. Favorites, hidden channels and the other app settings are kept in
each browser.

**The tuner** is an HDHomeRun (for example a FLEX DUO) with the antenna on its coax
input. Both containers use host networking so Tvheadend can find it by broadcast, on
the LAN or on a spare Ethernet port with link-local addressing that the firewall trusts.
For development without one, `AIRWAVES_DEMO=true` gives Tvheadend generated
test-pattern channels until a tuner is found.

When the HDHomeRun appears, `airwavesd` creates an "Airwaves antenna" network from the
US ATSC channel list, attaches its tuners, and lets Tvheadend scan. Channels are mapped as
they are found, and any demo channels are removed for good. The scan takes a few minutes;
if channels show up under the wrong numbers, check them in Tvheadend's Configuration >
Channel / EPG.

**The lineup is the tuner's.** The app's antenna channels are exactly the channels
Tvheadend's scan found; the FCC's records, the Gracenote listings and the ATSC 3.0 list
only describe them (call sign, network, transmitter, listings), never add one. Without a
tuner there are no antenna channels. A channel that won't lock right now stays listed,
and tuning it says so: "No signal on RF 31 (KTVD 20.x) right now".

**Signal is measured, never estimated.** `airwavesd` records, per RF channel, Tvheadend's
scan result and readings of the tuner (strength and quality as the HDHomeRun reports them,
in percent; SNR in dB, BER and uncorrected blocks from tuners that report them): every
10 seconds while a tuner is in use, and on request (`POST /api/signal/measure`, Measure
now), which briefly tunes each RF channel worth measuring on an idle tuner, at the lowest
weight a viewer's can have, so any viewer or recording takes the tuner over and the
sweep stops. Readings are kept in `<data>/signal.json` and served at `GET /api/signal`
and in the snapshot's `antenna` section. Terrain-based estimates are for planning an
antenna, in `otascan`, and aren't served to the app.

Recording rules are Airwaves': one airing, or a series by Gracenote series ID on one
channel, optionally new episodes only. Series rules skip episodes already recorded or
already scheduled. Every minute `airwavesd` turns the rules into timed Tvheadend entries
(1 minute early, 3 late, 30 late for sports) and removes entries for deleted rules.

## Without an antenna

`airwavesd` also runs on its own for the custom channels only (Jellyfin, YouTube,
folders of videos and the weather), with no antenna, tuner or Tvheadend. That is
`deploy/standalone`, built from this repository on any Docker host, amd64 or arm64;
[docs/self-hosting.md](docs/self-hosting.md) walks a newcomer through it. `scripts/deploy.sh`
and `deploy/compose.yml` are unchanged by it: every setting below defaults to what they
had.

| Setting | Does |
| --- | --- |
| `AIRWAVES_ANTENNA=off` | Custom channels only: no Tvheadend, demo channels, FCC or Gracenote lineup, terrain, reception reports or recording. `/api/info` says `"antenna": false` and the app hides Reception; what needs an antenna answers 501 with the reason. The location (`AIRWAVES_ZIP`, or `AIRWAVES_LAT` and `AIRWAVES_LON`) is only for the weather, and there's none unless given. |
| `AIRWAVES_HDHR` | `on` or `off`, the emulated HDHomeRun: on by default, off without an antenna. |
| `AIRWAVES_HDHR_DISCOVERY` | `on` or `off`, answering HDHomeRun discovery on the LAN (needs host networking): the same defaults. Without it, the device's URLs use the address clients were given (their `Host` header), since a container's port mapping changes its own. |
| `AIRWAVES_ADMIN_PASSWORD` | A password for `/admin/` and `/admin/mcp`: HTTP Basic auth with any user name, or `Authorization: Bearer <password>` from MCP clients. It's never logged, and the programs `airwavesd` runs don't inherit it. |
| `AIRWAVES_WEATHERSTAR_URL` | Where `airwavesd` reaches the WeatherStar display when that isn't 127.0.0.1 on `AIRWAVES_WEATHERSTAR_PORT` (`http://weatherstar:8080` for a container of its own). The weather channel is then on only while the display answers. |

`GET /healthz` answers `ok`, token or not, for container health checks (`airwavesd
-healthcheck` asks it). At startup `airwavesd` logs which features are on.

## Keys

| Key | TV |
| --- | --- |
| Enter (a remote's OK) | On-screen controls: the banner, a timeline, and buttons for the guide (first, so Enter twice opens it), pause, skip, live, captions, audio, record, favorite and last channel, with the views above. Arrows move, Enter presses, Esc puts them away; they stay up while paused |
| Up / Down, PgUp / PgDn | Channel up / down |
| Digits then Enter | Tune a channel, e.g. `7` `.` `2` |
| L or Backspace | Previous channel |
| Space | Pause and resume (live TV keeps a 30 minute rewind window) |
| Left / Right | Back 10 s / forward 30 s (Shift: 60 s), with the controls up on the timeline |
| End | Jump back to live |
| R / Shift R / N | Record this show / every episode / new episodes |
| C | Closed captions on or off |
| V | Next audio track (second language, described video) |
| f | Favorite this channel |
| I | Info banner; again for reception details, again to hide |
| M / Shift F | Mute / fullscreen |

| Key | Elsewhere |
| --- | --- |
| G | Guide: arrows move, Up from the top row to the filters (Favorites, Sports...; also `[` `]`). Enter watches what's on, or shows a program to come's actions; hold Enter, or I, for any program's: watch, record, series, new episodes only, favorite, hide. Home jumps to now, R / Shift R / N record, f favorite, H hide (U undoes) |
| D | Recordings: Left (or `[` `]`) for Library, Coming up, Series. Enter shows a recording's actions: resume, from the start, watched, delete (press twice); Shift Enter starts over, W marks watched, Delete twice removes; on a series K sets how many to keep, N new episodes only |
| A | Reception: arrows pick a transmitter; Left for the antenna presets (or `1` `2` `3`) and to preview any ZIP code, then copy a shareable summary |
| W | Weather: current conditions, alerts, the next 24 hours, 7-day, radar loop and satellite; Up and Down move through them, Left to watch the weather channel |
| `,` or S | Settings: server, antenna, guide hours, deleting watched recordings, hidden channels |
| Esc | Back to TV; from a recording, back to live |

With only a remote's arrows, OK and Back (an Android TV remote), every view is
reached from the views above it: Up from a view's top, or Left from its left
edge, and on TV, Up through the on-screen controls. Whatever the arrows are on has
an amber ring.

## Data sources

| Source | Used for | Cache |
| --- | --- | --- |
| [FCC TV Query](https://www.fcc.gov/media/television/tv-query) | Licensed transmitters: RF channel, ERP, height, location | 7 days |
| [AWS Terrain Tiles](https://registry.opendata.aws/terrain-tiles/) | Elevation along each path (SRTM, USGS NED and others) | permanent |
| [RabbitEars.Info](https://www.rabbitears.info/market.php?request=atsc3) | ATSC 3.0 hosts and services | 3 days |
| [Gracenote TV listings](https://tvlistings.gracenote.com/) | Lineup `USA-OTA<zip>` and schedules | 4 hours |
| [Zippopotam.us](https://zippopotam.us/) | ZIP centroid | 1 year |

Caches live in `~/Library/Caches/airwaves` (on the server, `~/airwaves/data/cache`);
app settings in `~/Library/Application Support/airwaves/settings.json`.

The Gracenote grid endpoint is the public JSON behind tvlistings.gracenote.com. It is
undocumented and meant for personal use; [Schedules Direct](https://www.schedulesdirect.org)
is the supported source if this grows beyond that.

## How reception is estimated

`internal/reception` computes free-space loss plus Deygout multiple knife-edge diffraction
over a terrain profile on a 4/3 effective earth, then compares the received power against
the ATSC 1.0 threshold (15.2 dB C/N over a 6 MHz noise floor, with extra man-made noise
on VHF). Each antenna preset adds its height, gain by band and wall or cable loss.

It captures line of sight and ridge blockage, which decide most outcomes along the Front
Range. It does not model multipath, buildings, foliage or directional transmit patterns,
so treat margins as a ranking and rough headroom rather than a promise. For an
authoritative second opinion, compare with the FCC DTV Reception Maps or RabbitEars'
Signal Search Map.

## Weather

`airwavesd` builds the weather report from public sources for the server's location:
NWS (api.weather.gov) for observations, the forecast, hourly data and alerts; the NWS
radar loop for the local radar site; NOAA GOES GeoColor satellite imagery; and
Open-Meteo for air quality and sunrise and sunset. Everything is cached on the server and
images are served from `/wximg/`, so the app never handles coordinates.

- **Weather channel**: 1.1 Airwaves Weather (WX), or whatever number and name the admin
  page gives it, is a WeatherStar 4000+ display ([ws4kp](https://github.com/netbymatt/ws4kp),
  MIT) running on the server with Airwaves branding (`deploy/weatherstar`).
  `/weatherstar` redirects to it with the server's location; the app shows it there, and
  HDHomeRun clients get it rendered to video (see Custom channels).
- **W page**: the weather report in the app.
- **Alert crawl**: NWS watches and warnings for the location crawl across live TV.

## HDHomeRun emulation

`airwavesd` also appears on the home network as an HDHomeRun network tuner, so other
players can watch the lineup: model HDTC-2US, friendly name "Airwaves", a device ID
derived from the server name, and as many tuners as Tvheadend
reports (2 until the real tuner is found). Antenna channels are passed through from
Tvheadend untouched (the original MPEG-TS, MPEG-2 video and AC-3 audio); custom channels
are H.264 and AAC in MPEG-TS at 1280x720.

It serves HTTP on port 5004 (`AIRWAVES_HDHR_LISTEN`, default `:5004`; empty or
`AIRWAVES_HDHR=off` disables it, and `AIRWAVES_HDHR_DISCOVERY=off` only its discovery):

| Path | Serves |
| --- | --- |
| `/discover.json`, `/lineup.json`, `/lineup_status.json` | The HDHomeRun device API |
| `/auto/v<number>`, `/tuner0/v<number>` | A channel's stream, e.g. `/auto/v9.1` |
| `/channels.m3u` | Playlist, with `url-tvg` pointing at the guide |
| `/xmltv.xml` | 48 hour guide from Gracenote listings and custom channel schedules; channel ids `airwaves.<number>` |
| `/channel-logos/<number>` | A custom channel's logo, also on the API port; the playlist and guide link to it |

Discovery answers on UDP 65001, only to other machines: requests from the server itself
are ignored so Tvheadend never adopts Airwaves as a tuner. Open TCP 5004 and UDP 65001
on the server's LAN interface only. With the server at 192.168.1.20:

- **Channels DVR**: found automatically as an HDHomeRun under Settings > Sources;
  otherwise use Add Source there to add it by IP, `192.168.1.20:5004`.
- **Jellyfin, Emby**: in Live TV, add a tuner device of type HDHomeRun with URL
  `http://192.168.1.20:5004`, and an XMLTV guide provider with
  `http://192.168.1.20:5004/xmltv.xml`.
- **HDHomeRun app**: discovers it on the same network.
- **VLC**: open the network stream `http://192.168.1.20:5004/channels.m3u`, or a single
  channel such as `http://192.168.1.20:5004/auto/v1.1`.

Plex is not a target.

## Custom channels

Custom channels have a guide schedule but no tuner behind them. `airwavesd` generates
them and decodes nothing unless someone is watching. They are in the HDHomeRun lineup
and guide, and in the app's guide, where pause and rewind work as for antenna channels;
they can't be recorded.

**Channel details**: every custom channel, the weather channel included, has a number
(1.0 to 999.999, kept as written, so 104.0 stays 104.0; no two custom channels share
one, however written: 1.05 is 1.5), a name, a short call sign for guide
rows and banners ("WX", "TOON"), a category (Kids, Family, Movies, Sports, News, Music,
Pets, Gaming, Documentary, Weather or Other), a description, a logo, and whether it's on
the air. A folder channel's number and name are its folder's name; the rest is in
`channel.json` in the folder, and its logo is an image there (`logo.png`, `.svg`, `.jpg`
or `.webp`, found by name or named in `channel.json`):

```json
{ "callSign": "TOON", "category": "Kids", "description": "Saturday morning cartoons.", "logo": "logo.png", "enabled": true }
```

The weather channel has no folder, so its whole record, number and name included, is
`.weather.json` in the channels folder (defaults: 1.1 "Airwaves Weather", WX, Weather),
with its logo beside it as `.weather-logo.png`. Changes are picked up on the next
request, with no restart; a stream already playing carries on. Channels turned off
(`"enabled": false`) leave the HDHomeRun lineup and the app. A custom channel may take an
antenna channel's number, and takes its place in the HDHomeRun lineup; the admin page
warns when it does. The HDHomeRun playlist keeps custom channels in the "Airwaves" group,
apart from the antenna's, and gives their logos as `tvg-logo`; the XMLTV guide lists the
call sign among each channel's names, links the logo as its icon, and gives every
programme without a category of its own the channel's (none for Other).

The app lists custom channels among the antenna channels by number (a custom channel
takes the place of an antenna channel with the same number there too), with their call
signs and logos in the guide and the banner, and their descriptions when nothing is
listed. A category counts as a genre of the channel's programs for the guide's filters,
and categories with no filter of their own (Kids, Music, Pets, Gaming, Documentary) get
one while a channel has them. Favorites and hidden channels follow a custom channel to a
new number or name.

**1.1 Airwaves Weather** is the WeatherStar 4000+ display rendered by headless Chromium
inside the `airwavesd` container and encoded with ffmpeg, over looping background music.
Chromium only runs while someone is watching and is stopped afterwards (the compose
service sets `init: true` so its helper processes are reaped). The guide has an
hour-long "Local Forecast" block each hour; its subtitle is that hour's forecast (e.g.
"66° and Clear", prefixed with an active NWS alert) and its description is the NWS
forecast period text.

By default the four royalty-free tracks bundled with ws4kp loop. MP3s in the music
folder (`MUSIC` in `~/airwaves/.env`, mounted at `/music`; `AIRWAVES_MUSIC`) are used
instead, looped in file name order.

**Folder channels**: every subfolder of the channels folder is a channel (`CHANNELS` in
`~/airwaves/.env`, such as `/srv/airwaves/channels`, mounted at `/channels`;
`AIRWAVES_CHANNELS`). The folder name sets the number and name:
`1.2 Cat Calming` is channel 1.2 "Cat Calming"; folders without a leading number, or
whose number is the weather channel's, get the next free 1.x numbers. A folder with no
playable videos is left out of the lineup.

Videos (mp4, mkv, webm, mov, m4v, ts) play in file name order and loop forever on a fixed
clock, so the guide is accurate and tuning in joins whatever is on part way through.
Changing a folder's contents reshuffles its schedule. Partial yt-dlp downloads (`.part`,
`.ytdl`, `.temp` and `.fNNN` intermediate files) are ignored, and new files are noticed
within about a minute, with no restart. Guide titles come from the yt-dlp `.info.json`
next to each video (the title, and the first paragraph of the description); without
one, the file name minus its `[video id]` suffix is used. Every video is scaled and
letterboxed to 1280x720 at 30 fps with stereo AAC; videos without audio get silence.

To fill a channel with yt-dlp (H.264 at up to 1080p keeps decoding cheap):

```sh
cd "/srv/airwaves/channels/1.2 Cat Calming"
yt-dlp -S "res:1080,vcodec:h264,acodec:m4a" --merge-output-format mp4 --write-info-json \
  -o "%(title)s [%(id)s].%(ext)s" "<video or playlist URL>"
```

YouTube needs a current yt-dlp (older packaged copies get HTTP 403 on downloads), with
deno for YouTube's JavaScript challenges; `yt-dlp -U` updates the release builds.

**Jellyfin channels**: a channel folder holding `jellyfin.json` plays from a Jellyfin
server instead of its own videos, the way ErsatzTV builds channels from a media library.
The series, movies, collections, playlists and libraries it names loop on a fixed clock
with a full guide (series and episode titles, season and episode numbers, descriptions,
artwork from the server), streamed from the server only while someone watches.

```json
{
  "series": ["Futurama", "Batman: The Animated Series (1992)"],
  "movies": ["The Iron Giant", "The Thing (1982)"],
  "collections": ["Saturday Morning"],
  "playlists": [],
  "libraries": [],
  "order": "shuffle",
  "maxBitrate": 0
}
```

Names match Jellyfin's, ignoring case; a trailing year picks between same-named titles.
Names that match nothing are logged. Only episodes and movies play, and episodes the
server has no file for are skipped. `order` is `shuffle` (the default: a fixed shuffle
per channel) or `aired` (each series in season and episode order, movies by release
date; for a one-show channel, its episodes in order). The list is refreshed about every
30 minutes, and the schedule only changes when the set of videos does: adding or
removing one reshuffles it.

The account goes in `.jellyfin.json` in the channels folder, shared by every Jellyfin
channel, which sign in once between them:

```json
{ "server": "https://jellyfin.example.com", "user": "airwaves", "password": "..." }
```

`token` (an API key, or one from Quick Connect with its `userId` and `deviceId`) can
replace the user and password, and a channel's own `jellyfin.json` can name a different
`server`, `user` and `password` or `token`. These files live only on the server, in the
channels folder; the password and token stay out of logs, the guide and the app. Ask
your friend for a Jellyfin user just for Airwaves, with access to only what the channels
need. If the server refuses the password, Airwaves stops trying until one of the files
changes, since repeated failures lock a Jellyfin account.

Bandwidth: with `maxBitrate` 0 the original files stream as they are (a 1080p movie is
often 5 to 20 Mb/s) and `airwavesd` decodes them; a `maxBitrate` in Mb/s (say 4) has the
Jellyfin server transcode to H.264 and AAC at that rate and at most 720p, on its CPU.
Either way it is one stream per viewer, and none while no one watches.

**YouTube channels**: a channel folder holding `youtube.json` runs the uploads of YouTube
channels like a TV channel, with a real guide, and downloads nothing. For example
`1.8 Northernlion/youtube.json` in the channels folder:

```json
{
  "channels": ["https://www.youtube.com/@Northernlion", "https://www.youtube.com/@TheLibraryofLetourneau"],
  "repeatDays": 30,
  "maxHeight": 720,
  "minMinutes": 3,
  "maxMinutes": 240,
  "rerunMix": "balanced",
  "lockHours": 2,
  "maxAgeDays": 0,
  "deadAir": false
}
```

`channels` are channel URLs, `@handle`s or `UC...` channel IDs. No video airs again within
`repeatDays` while the channels have enough videos (then the one aired longest ago goes
next). With `deadAir` true none ever does: a YouTube channel with nothing left to air sits
out its turn, and when every video has aired, the channel shows "Please stand by" ("Off
air" in the guide) until a new upload arrives or a video's repeat window passes. `maxHeight` caps the picture streamed (H.264 and AAC). Videos shorter than
`minMinutes` or longer than `maxMinutes` are left out (`maxMinutes` 0 for no limit), as
are Shorts, live streams and upcoming premieres. `rerunMix` leans reruns towards recent
uploads: `recent` picks about 70% of them from the last six months, 20% from the last
two years and 10% from any time; `balanced` 40/30/30; `any` all videos alike. When none
from the last six months (or two years) is free of the repeat guard, the pick looks
further back. `lockHours` is how far ahead new uploads leave the schedule as it is.
`maxAgeDays` plays only videos uploaded within that many days (0 for any age), aging
them out as days pass; with none that recent, the channel leaves the lineup until a new
upload arrives. The values shown are the defaults.

A YouTube channel can play a playlist instead, in its order, looping at the end:

```json
{ "playlist": "https://www.youtube.com/playlist?list=PL8dPuuaLjXtNlUrzyH5r6jN9ulIgZBpdo", "maxHeight": 720 }
```

`playlist` is the playlist's link, the link of a video played from it (anything with
`list=`), or its ID; a `youtube.json` naming both channels and a playlist is refused. It
runs like a folder channel, looping on a fixed clock or airing on a schedule (see
Schedules below), so there's no planner, first runs or repeat guard, no `.playout.json`,
and of the settings only `maxHeight` applies. Private, deleted and members-only videos
are left out; live streams and premieres join once they can play. The playlist is listed
when the folder appears, then daily and whenever `youtube.json` changes, so videos added
to it join where the playlist has them; `.catalog.json` keeps them in order, and a length
the listing lacks is looked up. Its videos stream exactly as uploads do, captions, sound
language and leveling included.

`airwavesd` lists each channel's uploads with yt-dlp (titles, lengths and rough upload
dates, from YouTube's "3 years ago") when the folder appears and then daily;
Northernlion's 22,000 videos take about five minutes, and the channel joins the lineup
once every channel is listed. Every 15 minutes it reads the channels' RSS feeds for new
uploads and looks up their lengths and exact dates. The catalog is kept in the folder as
`.catalog.json`, and the schedule as `.playout.json`: two days ahead plus the history the
repeat guard reads, so the guide holds across restarts (delete it to start the schedule
afresh). Automatic updates only add to the schedule: what's on and the next `lockHours`
stay as they are, and new uploads go in as first runs (marked new in the guide) right
after, newest first, pushing the planned reruns later. Editing `youtube.json` instead
plans it all again from that moment, what's on included (a viewer already watching
finishes the current video first). Reruns alternate between the channels, so each gets
as many slots whatever its size; each picks at random by the rerun mix among its videos
that haven't aired within `repeatDays`, avoiding the game or series of the last few
airings (guessed from titles). Guide entries have the video's title, the channel's name
with the upload date ("Northernlion, Mar 4, 2014"; rough dates as "2014", or "March
2026" within the last year), the first paragraph of its description (looked up a few at
a time for what airs next) and its YouTube thumbnail.

Only while someone watches, yt-dlp finds the video's stream and ffmpeg plays it from
where the schedule is, in real time, like any other custom channel: tuning in takes about
four seconds, and the next video is found a minute before it starts. A video that won't
play (removed, private, or refused by YouTube) gets a dark "Please stand by" slate for its
slot and the channel carries on with the next one on time; removed videos are dropped
from the rest of the schedule.

YouTube changes often and older yt-dlp versions stop working, so `airwavesd` keeps its
own copy of the latest release at `/data/tools/yt-dlp`: downloaded on first use, checked
against the release's checksums, and replaced when a newer release is out (checked
daily), with no image rebuild. The image has python3 to run it and deno for YouTube's
JavaScript challenges. `AIRWAVES_YTDLP` (or `-ytdlp`) names a different yt-dlp instead,
which is then left as it is. `airwavesd` (uid 1000) needs write access to the channel
folder.

**Schedules**: folder and Jellyfin channels, and YouTube channels playing a playlist,
play their videos in order, looping around the clock. A `schedule` in the channel's
`channel.json` airs them on a timetable instead, so a series can be watched as it comes:
four episodes of House of the Dragon a night from the first is

```json
{ "schedule": { "start": "2026-10-04", "first": 1, "blocks": [{ "at": "20:00", "count": 4 }] } }
```

`start` is the day it begins (or `"2026-10-04T20:00"` for a time that day), in the
server's time zone, and `first` the video it begins with, counting from 1 in the
channel's order (file name order for a folder; for a series on Jellyfin, `"order":
"aired"`). A block airs `count` videos back to back from `at`, or starts them until
`until` (`"23:30"`; earlier than `at` is the next day's) and lets the last one finish;
`days` (`["sat", "sun"]`) keeps it to those days, every day without. Each block picks up
where the last left off, and the list loops at the end. Between blocks the channel is
off the air but stays in the lineup: the guide says "Off air" and what's next ("Back at
8:00 PM with House of the Dragon, S1 E4."), and tuning in shows a dark slate saying when.
Without blocks it airs around the clock from `start`. Changes show in the guide at once,
and in a stream already playing at its next video (within a minute while it's off the
air). The admin page sets it all under Airs.

**Loudness**: with `AIRWAVES_LOUDNESS` set to a level in LUFS (-24 is broadcast TV's,
ATSC A/85), custom channels' sound is evened out to it, so a loud YouTube upload doesn't
follow a quiet Jellyfin episode; it's `off` by default. Nothing extra is read for it: the
sound is metered as it streams, in the samples on their way to the encoder (ITU-R
BS.1770: K-weighted, gated loudness), and turned up or down toward the level, slowly (no
more than 3 dB a second, settling within about 15 seconds of a new video) and within -20
to +12 dB, with raised peaks held under -1 dBFS. What each video measured is remembered
(`.loudness.json` in a folder or Jellyfin channel's folder, the catalog for YouTube), so
its next airing starts at the right level; until then YouTube videos start 10 dB down
(YouTube's usual -14 LUFS) and the rest as they are. The weather channel's music is
leveled the same way.

## Admin page

`http://<server>:8089/admin/` (for example `http://nas:8089/admin/`) is for the
server's owner, not the app's viewers. Every custom channel's page starts with its
details: number and name (a folder channel's rename its folder), call sign, category,
description, on the air or not, and a logo (upload or drop an image, take the poster of
a Jellyfin channel's first pick or a YouTube channel's avatar, or remove it), with a
warning when the number is an antenna channel's too. Below that it edits Jellyfin
channels: sign in to Jellyfin (with a Quick Connect code from any signed-in Jellyfin app,
or a user and password), browse the server's series, movies, collections and playlists
as posters with search and genre filters, pick exactly what each channel plays, set its
order and bitrate cap, and see what airs next; and YouTube channels, found by search or
by link, with their length, repeat, age and quality settings, or a playlist by its link. Folder, Jellyfin and YouTube
playlist channels also say when they air: around the clock, or on a schedule (a start
date, the video to begin with, and daily blocks), summed up in a line as it's edited.
It writes the same `channel.json`, `.weather.json`, `jellyfin.json`, `youtube.json` and
`.jellyfin.json` files described above (account files are readable only by the server's
user), so hand edits and the page agree.

**Backups**: so the channel setup never has to be made again by hand, `airwavesd` keeps
backups of it, under Backups below the account on the page. A backup is a small `.tar.gz`
holding every channel folder's name (folders of videos too, so they can be made again),
each one's `jellyfin.json`, `youtube.json`, `channel.json`, logo and YouTube schedule
(`.playout.json`), and the channels folder's `.jellyfin.json` (the Jellyfin account),
`.weather.json` and weather logo, with a `manifest.json` saying when, why and by which
`airwavesd` it was made and what's in it. Videos, YouTube catalogs (listed again) and
yt-dlp's `.info.json` files are never in one. A backup is made 30 seconds after changes
through the page or the agent tools (a burst of changes makes one), before every restore,
and daily (a minute after `airwavesd` starts, then every day) for changes made some other
way; an automatic one that would hold the same as the newest is skipped. The newest 30
automatic backups are kept, and the newest of each of the last 14 days; older ones are
removed. Those made with Back up now (with an optional note) or uploaded stay until
deleted. The page lists them with download, delete and restore.

Restoring asks what to restore (everything or chosen channels, the Jellyfin account or
not, YouTube schedules or not) and says what happens to each channel before anything
changes. It goes folder by folder: a channel in the backup is the folder of the same
name; failing that, a Jellyfin or YouTube channel renamed since is the one folder playing
the same YouTube channels or Jellyfin picks, and gets its old number and name back;
failing that, its folder is made again (a folder channel's empty: put its videos back).
The folder gets the backup's settings, details and logo, and loses those added since;
videos, catalogs and anything else in it stay. A YouTube schedule comes back only where
the channel has none, so channels on the air keep their guide and remade ones pick up
where the backup left off. A channel whose number another channel has now is left out
until that one moves, and channels the backup doesn't have are left alone. The setup as
it was is backed up first, so restoring that undoes a restore. Uploaded backups are
checked first (no paths outside the channels folder, links or devices; only the files a
setup has; under 16 MB, and 64 MB unpacked), and older snapshots made by hand (a
`.tar.gz` of the channels folder's settings and logos, without a manifest) restore the
same: upload one on the page, or copy it into the backups folder.

The backups are in `~/airwaves/data/backups` (`/data/backups` in the container), readable
only by the server's user. To keep copies off the server, download them from the page, or
copy the folder:

```sh
rsync -a nas:airwaves/data/backups/ ~/airwaves-backups/
```

Without `AIRWAVES_ADMIN_PASSWORD` the page has no login of its own: it is only as private
as the address `airwavesd` listens on, which `deploy.sh` makes the tailnet address,
never the LAN, so never expose it beyond a network you trust. With a password, the page, its API
and `/admin/mcp` ask for it (HTTP Basic auth, any user name).

## Agents

AI agents manage the custom channels with the page's tools, two ways. Both run the
page's own API, so its checks apply and hand edits, the page and agents agree:
`list_channels`, `get_channel`, `get_schedule`, `list_antenna_channels`,
`browse_jellyfin`, `search_youtube_channels`, `lookup_youtube_channel`,
`create_youtube_channel`, `update_youtube_channel`, `create_jellyfin_channel`,
`update_jellyfin_channel`, `set_channel_details`, `delete_channel` (which takes
`confirm: true`), `list_backups` and `create_backup` (restoring a backup is left to a
person, on the page). `airwavesd` logs each call with the tool and channel number only.

**MCP**: `airwavesd` serves the tools at `http://<server>:8089/admin/mcp` (Streamable
HTTP, stateless, so clients carry on across restarts). For Claude Code:

```sh
claude mcp add --transport http airwaves http://nas:8089/admin/mcp
```

**WebMCP**: the page registers the same tools with WebMCP (`document.modelContext`), and
shows what an agent changes as it happens. An agent on the computer with the page open
reaches them through the WebMCP local relay, added as an MCP server:

```sh
claude mcp add webmcp -- npx -y @mcp-b/webmcp-local-relay@5.1.0 --widget-origin http://localhost:8089
```

WebMCP needs a secure context, so open the page through a tunnel
(`ssh -L 8089:<tailnet address>:8089 nas`, then `http://localhost:8089/admin/`) or over
HTTPS; over plain HTTP to the tailnet address the page leaves WebMCP off. The page only
connects to the relay on 127.0.0.1:9333, and without one it works as ever (the browser
logs a few refused connections, then stops trying).

Like the page, the endpoint has no login unless `AIRWAVES_ADMIN_PASSWORD` is set (then add
`--header "Authorization: Bearer <password>"` to `claude mcp add`): without one, anyone
who can reach `airwavesd` can change channels with it, so keep it on the tailnet address. Other sites' browser requests are
refused, and the Jellyfin account isn't among the tools.

## Playback

Live channels and recordings are read from Tvheadend as MPEG-TS and transcoded by
`airwavesd` to H.264 and AAC in HLS, which the app plays natively. Custom channels come
from `airwavesd` itself and play the same way. ATSC 1.0 (MPEG-2 video, AC-3 audio) plays
fully; ATSC 3.0 is out of scope (AC-4 audio and A3SA DRM).

## Layout

```
main.go, app.go           Wails entry point; the app is a client of airwavesd
cmd/airwavesd             the home server
cmd/otascan               terminal report
internal/service          the engine airwavesd runs
internal/api              HTTP API, and a client with the same interface
internal/dvr              recording rules and reconciliation with Tvheadend
internal/tvh              Tvheadend API: channels, DVR entries, network setup, input status; tvhtest fakes it
internal/fcc              FCC TV Query client and parser
internal/terrain          Terrarium elevation tiles and path profiles
internal/reception        propagation model and antenna presets
internal/atsc3            RabbitEars ATSC 3.0 list parser
internal/guide            Gracenote lineup and listings
internal/lineup           merges all of the above into a Report, and describes the tuner's channels from it
internal/signal           what the tuners measured per RF channel, kept across restarts
internal/tuner            Tvheadend input, demo test patterns
internal/stream           ffmpeg to HLS, one session per client
internal/hdhr             emulated HDHomeRun: discovery, lineup, streams, XMLTV
internal/vchan            custom channels: Airwaves Weather, folder, Jellyfin and YouTube channels
internal/jellyfin         Jellyfin client: sign-in, Quick Connect, browsing, stream URLs
deploy/, scripts/         Docker files for the server and the deploy script
deploy/standalone         Docker Compose for custom channels only, built from source (docs/self-hosting.md)
frontend/dist             the UI: plain HTML, CSS and JS, no build step; also served at /tv/
```

`go test ./...` covers the parsers, the propagation model, recording rules, the tuner
lineup, signal measuring and demo cleanup against a fake Tvheadend (answering with a real
one's JSON), the API client and server, the emulated HDHomeRun's discovery and HTTP
API, and an end-to-end ffmpeg HLS stream.

## License

MIT, see `LICENSE`. Bundled libraries, fonts and icons keep their own licenses, listed in
`THIRD_PARTY_NOTICES.md`.
