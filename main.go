// Command airwaves is the desktop TV for an Airwaves server: live
// over-the-air TV, the program guide, recordings and reception.
package main

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"airwaves/frontend"
)

const version = "0.1.0"

func main() {
	app := NewApp()
	setPageZoom(float64(app.scale()) / 100)
	width, height, state := window()
	err := wails.Run(&options.App{
		Title:     "Airwaves",
		Width:     width,
		Height:    height,
		MinWidth:  min(1024, width),
		MinHeight: min(640, height),
		// Filling the screen, or 16:9 under gamescope (see window).
		WindowStartState: state,
		BackgroundColour: &options.RGBA{R: 7, G: 8, B: 10, A: 255},
		AssetServer:      &assetserver.Options{Assets: frontend.Dist()},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []any{app},
		Mac: &mac.Options{
			TitleBar:   mac.TitleBarHiddenInset(),
			Appearance: mac.NSAppearanceNameDarkAqua,
			About: &mac.AboutInfo{
				Title:   "Airwaves " + version,
				Message: "Over-the-air TV, guide and reception explorer.",
			},
		},
		// Without Linux options Wails turns WebKitGTK's GPU use off, and
		// video drawn in software on a TV runs at a few frames a second.
		Linux: &linux.Options{
			WebviewGpuPolicy: linux.WebviewGpuPolicyAlways,
			ProgramName:      programName(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}

// window is the size and state the window opens in. On a desktop it fills
// the screen, zoomed (not a fullscreen space). Under gamescope (SteamOS
// Game Mode), which has no window manager and scales the game's window to
// the TV on the GPU, it is a plain 16:9 1920x1080 window: going fullscreen
// there would render the page at the TV's own size, which can be 4K. Shift
// F still goes fullscreen. AIRWAVES_WINDOW=1280x720 opens a window of that
// size instead, for weaker boxes.
func window() (width, height int, state options.WindowStartState) {
	if w, h, ok := parseSize(os.Getenv("AIRWAVES_WINDOW")); ok {
		return w, h, options.Normal
	}
	if gamescope() {
		return 1920, 1080, options.Normal
	}
	return 1360, 840, options.Maximised
}

// autoScale is the interface size for the screen, in percent: larger under
// gamescope, where the 1080p window fills a TV across the room.
func autoScale() int {
	if gamescope() {
		return 125
	}
	return 100
}

// gamescope tells whether the app runs under gamescope: SteamOS Game Mode,
// on a TV.
func gamescope() bool {
	return os.Getenv("GAMESCOPE_WAYLAND_DISPLAY") != "" || os.Getenv("SteamGamepadUI") != "" ||
		os.Getenv("XDG_CURRENT_DESKTOP") == "gamescope"
}

// programName is the Flatpak's app ID when there is one. WebKit names its
// MPRIS service (the desktop's now playing and media keys) after the
// program name when that is a valid application ID, and Flatpak lets an
// app own MPRIS names under its own ID only.
func programName() string {
	if id := os.Getenv("FLATPAK_ID"); id != "" {
		return id
	}
	return "airwaves"
}

// parseSize reads "1280x720"; an empty or malformed size is not ok.
func parseSize(s string) (width, height int, ok bool) {
	if s == "" {
		return 0, 0, false
	}
	ws, hs, _ := strings.Cut(strings.ToLower(strings.TrimSpace(s)), "x")
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	if err1 != nil || err2 != nil || w < 320 || h < 240 {
		log.Printf("AIRWAVES_WINDOW=%q: want WIDTHxHEIGHT, like 1280x720", s)
		return 0, 0, false
	}
	return w, h, true
}
