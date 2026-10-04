# Third-party notices

Airwaves is MIT licensed (see `LICENSE`). It bundles the following, each under
its own license, kept beside it in the tree.

| What | Where | License |
| --- | --- | --- |
| [hls.js](https://github.com/video-dev/hls.js) 1.7.3 | `frontend/dist/vendor/hls.min.js` | Apache-2.0, `frontend/dist/vendor/hls.js-LICENSE.txt` |
| [Pretext](https://github.com/chenglou/pretext) 0.0.9 | `frontend/dist/vendor/pretext/` | MIT, `frontend/dist/vendor/pretext/LICENSE` |
| [Kenney Input Prompts](https://kenney.nl/assets/input-prompts) 1.5 | `frontend/dist/vendor/kenney-prompts/` | CC0, `frontend/dist/vendor/kenney-prompts/License.txt` |
| [@mcp-b/global](https://www.npmjs.com/package/@mcp-b/global) and [@mcp-b/webmcp-local-relay](https://www.npmjs.com/package/@mcp-b/webmcp-local-relay) 5.1.0 | `internal/admin/ui/vendor/mcp-b/` | MIT, `internal/admin/ui/vendor/mcp-b/LICENSE` |
| [Wails](https://wails.io) runtime bindings | `frontend/wailsjs/` | MIT, `frontend/wailsjs/LICENSE` |
| [Big Shoulders Display](https://github.com/xotypeco/big_shoulders) | `frontend/dist/fonts/`, `internal/admin/ui/fonts/` | SIL OFL 1.1, `OFL-BigShouldersDisplay.txt` there |
| [IBM Plex Mono](https://github.com/IBM/plex) | `frontend/dist/fonts/`, `internal/admin/ui/fonts/` | SIL OFL 1.1, `OFL-IBMPlexMono.txt` there |
| [Schibsted Grotesk](https://github.com/schibsted/schibsted-grotesk) | `frontend/dist/fonts/`, `internal/admin/ui/fonts/` | SIL OFL 1.1, `OFL-SchibstedGrotesk.txt` there |

Go modules are listed in `go.mod` and keep their own licenses.

The deployment builds or runs these from their upstream images and releases
rather than shipping them: [Tvheadend](https://tvheadend.org) (GPL-3.0),
[WeatherStar 4000+](https://github.com/netbymatt/ws4kp) (MIT, with Airwaves
branding swapped in by `deploy/weatherstar/Dockerfile`),
[yt-dlp](https://github.com/yt-dlp/yt-dlp) (Unlicense), [Deno](https://deno.com)
(MIT), [FFmpeg](https://ffmpeg.org) and Chromium.

Test fixtures hold small excerpts of public data for parsing tests: FCC station
records, a RabbitEars ATSC 3.0 table, YouTube listing metadata and an
auto-caption excerpt.
