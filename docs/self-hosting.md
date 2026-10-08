# Running Airwaves without an antenna

This sets up an Airwaves server for its own channels only, with no antenna or tuner:

- **Jellyfin channels**: series, movies and collections from a Jellyfin server, on a
  schedule, with a guide.
- **YouTube channels**: the uploads of YouTube channels you pick, streamed as they air.
  Nothing is downloaded.
- **Folder channels**: a folder of your own videos, on a loop.
- **The weather channel**: a WeatherStar 4000+ style local forecast (US locations).

You watch them in the Airwaves desktop app, and, if you turn it on, in Jellyfin, Emby,
Channels DVR or VLC through the emulated HDHomeRun.

It runs in Docker on a computer that stays on: a Linux PC or mini PC, a NAS that runs
Docker, a Mac or Windows PC with Docker Desktop, or a Raspberry Pi 4 or 5 with a 64-bit
OS. Both amd64 (Intel, AMD) and arm64 (Apple Silicon, Raspberry Pi) work. Each channel
someone is watching costs one 720p video encode, so a small computer manages a viewer or
two; nothing runs while no one watches.

## What you need

- Docker with Compose: [Docker Desktop](https://www.docker.com/products/docker-desktop/)
  on a Mac or Windows PC, or Docker Engine on Linux. `docker compose version` should say
  v2.24 or newer.
- git, to fetch Airwaves and update it.
- About 1.5 GB of disk for the images, plus room for any videos of your own.
- The Airwaves desktop app on the computer you watch on.

## Set it up

1. **Get Airwaves** and go to its standalone setup:

   ```sh
   git clone https://github.com/FelineStateMachine/airwaves.git airwaves
   cd airwaves/deploy/standalone
   ```

2. **Write your settings.** Copy the example file, then open `.env` in a text editor:

   ```sh
   cp .env.example .env
   ```

   Every setting is explained in the file. The ones to look at first:

   - `AIRWAVES_ZIP`: your ZIP code, so the weather is yours (or `AIRWAVES_LAT` and
     `AIRWAVES_LON` for a precise spot). Leave it empty to go without weather.
   - `TZ`: your time zone, such as `America/Chicago`.
   - `AIRWAVES_ADMIN_PASSWORD`: a password for the admin page. Set one unless this
     computer is only on a home network you trust (see [Keeping it private](#keeping-it-private)).
   - `PUID` and `PGID` (Linux): the user and group that own the `data` and `channels`
     folders. `id -u` and `id -g` print yours; the default, 1000, is usually right.

3. **Make the folders** it keeps things in, next to `compose.yml`:

   ```sh
   mkdir -p data channels
   ```

4. **Start it.** The first time builds the images, which takes a few minutes (longer on
   a Raspberry Pi):

   ```sh
   docker compose up -d --build
   ```

   It starts again by itself after a reboot, as long as Docker does (in Docker Desktop's
   settings, turn on starting it when you sign in). To see what it's doing:

   ```sh
   docker compose logs -f airwavesd
   ```

   The first lines say what's on, for example:

   ```
   airwavesd 0.2.0 listening on :8089
   antenna: off, custom channels only
   location: ZIP 80302
   weather channel: WeatherStar display at http://weatherstar:8080 (port 8090 for the app), on while it answers
   custom channels: /channels
   admin page: /admin/, with a password
   app API token: off
   yt-dlp: /data/tools/yt-dlp, kept up to date
   HDHomeRun: off
   weather channel: on, WeatherStar display at http://weatherstar:8080
   ```

   Press Ctrl-C to stop following the log; Airwaves keeps running.

5. **Open the admin page** in a browser: `http://<this computer>:8089/admin/`, for
   example `http://192.168.1.20:8089/admin/` or, on the computer itself,
   `http://localhost:8089/admin/`. With a password set, the browser asks for it (any user
   name will do).

6. **Add channels** on the admin page with **New channel**:

   - **YouTube**: search for a channel or paste its link. Airwaves lists its uploads
     (a few seconds for most channels, minutes for very large ones), then the channel
     joins the lineup.
   - **Jellyfin**: first sign in to your Jellyfin server in the panel on the left (with a
     Quick Connect code from any signed-in Jellyfin app, or a user and password). Then
     pick the series, movies and collections the channel plays. A Jellyfin user just for
     Airwaves, allowed only what the channels need, is a good idea.
   - **A folder of videos**: make a folder in `channels` named with a number and a name,
     such as `channels/2.1 Cartoons`, and put videos in it (mp4, mkv, webm, mov, m4v or
     ts). It joins the lineup within a minute.

   Every channel gets a number, a name, a call sign, a category, a description and a
   logo, all on the admin page. The weather channel is 1.1 unless you change it.

7. **Point the Airwaves app at it.** In the app's Settings, set **Airwaves server** to
   this computer's address, such as `192.168.1.20` or `nas.local` (port 8089 is assumed;
   add `:<port>` if you changed `PORT`). With `AIRWAVES_TOKEN` set, enter it as the
   token. The guide then shows your channels. Without an antenna the app has no
   Reception or Recordings view. A browser works too, with nothing to install: open
   `http://<this computer>:8089/tv/`, such as `http://192.168.1.20:8089/tv/`.

## Jellyfin, Emby, Channels DVR and VLC

These play Airwaves' channels through an emulated HDHomeRun tuner, which is off unless
you want it. Set `AIRWAVES_HDHR=on` in `.env`, then `docker compose up -d`. Then, with
`<host>` this computer's address and 5004 the `HDHR_PORT`:

- **Jellyfin or Emby**: in Live TV, add a tuner device of type HDHomeRun with the URL
  `http://<host>:5004`, and a TV guide data provider of type XMLTV with
  `http://<host>:5004/xmltv.xml`.
- **Channels DVR**: Settings, Sources, Add Source, HDHomeRun, with `<host>:5004`.
- **VLC**: open the network stream `http://<host>:5004/channels.m3u`.

(This is Jellyfin as a player of Airwaves' channels; Jellyfin as a library that Airwaves
channels play from is set up on the admin page, as above.)

Players can also find the tuner by themselves on the LAN, with HDHomeRun discovery. That
needs host networking, on Linux only: add `COMPOSE_FILE=compose.yml:compose.host.yml` to
`.env` (and leave `BIND` unset), then `docker compose up -d`. Most players are just as
happy with the address typed in.

## Keeping it private

- **The admin page** (`/admin/`) and its tool endpoint for AI agents (`/admin/mcp`)
  change your channels and hold your Jellyfin sign-in. Without `AIRWAVES_ADMIN_PASSWORD`,
  anyone who can reach port 8089 can use them, so leave the password empty only when
  this computer is on a home network you trust, never one shared with strangers.
- **Never forward the ports to the internet.** To watch away from home, use a VPN such
  as [Tailscale](https://tailscale.com) and set `BIND` to the computer's Tailscale
  address, so Airwaves listens there only. The admin password travels unencrypted over
  plain HTTP, which is fine at home or over a VPN but not across the internet.
- **Watching**: anyone who can reach port 8089 can watch, unless you set
  `AIRWAVES_TOKEN`; then the app needs the same token in its Settings.
- **AI agents**: to let an agent manage channels over MCP with a password set, give it
  the password as a bearer token, for example
  `claude mcp add --transport http airwaves http://<host>:8089/admin/mcp --header "Authorization: Bearer <password>"`.

## Updating

From `airwaves/deploy/standalone`:

```sh
git pull
docker compose up -d --build
```

Your channels and settings stay: they're in `channels` and `data`.

YouTube changes often, and older versions of yt-dlp (the tool Airwaves uses to play
YouTube) stop working. Airwaves keeps its own copy in `data/tools` and replaces it when a
new release comes out (it checks daily), so there's no need to update Airwaves for that.
If YouTube channels stop playing anyway, delete `data/tools/yt-dlp`; Airwaves downloads
the latest one the next time it needs it.

## Backing up

- Airwaves keeps backups of the channel setup in `data/backups` (each channel's
  settings, logo and schedule, and the Jellyfin sign-in; no videos), and the admin page
  shows them. Copy that folder somewhere else now and then, and keep the copy private,
  since it holds the Jellyfin sign-in.
- Videos of your own in `channels` are yours to back up as you would any files.
- The rest of `data` (caches, yt-dlp) is made again as needed.

## When something's wrong

- **Look at the log first**: `docker compose logs --tail 100 airwavesd`.
- **"can't write to /data" (or /channels)**: the folder belongs to another user. Set
  `PUID` and `PGID` to its owner, or give it to the user Airwaves runs as:
  `sudo chown -R 1000:1000 data channels`. Then `docker compose up -d`.
- **No weather channel**: the log says why. It needs a location (`AIRWAVES_ZIP`, or
  `AIRWAVES_LAT` and `AIRWAVES_LON`) and the display (`COMPOSE_PROFILES=weather`). To go
  without it, put a `#` before `COMPOSE_PROFILES=weather` and run
  `docker compose rm -sf weatherstar`.
- **"port is already allocated"**: another program uses that port. Pick another `PORT`,
  `WEATHERSTAR_PORT` or `HDHR_PORT` in `.env`.
- **The app can't connect**: check the address and port, that the computer's firewall
  lets the port in, and `docker compose ps` (airwavesd should say "healthy").

## Building the image elsewhere

`compose.yml` builds the image on the computer it runs on. To build it once for both
kinds of computer and push it to a registry instead (from the repository root):

```sh
docker buildx build --platform linux/amd64,linux/arm64 -f deploy/standalone/Dockerfile \
  -t <registry>/airwavesd:latest --push .
```

then set `image:` to that name in `compose.yml` and drop its `build:` section. The image
runs as a regular user, not root, and checks its own health (`airwavesd -healthcheck`).
