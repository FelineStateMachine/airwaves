# Airwaves for Android TV

A thin shell around the server's TV page: a fullscreen WebView on
`<server>/tv/` (airwavesd serves it), with a native setup screen for the server
address. Android 9 or later, for Google TV and Android TV.

## Build

Needs a JDK 17 or later and the Android SDK (platform 36), found through
`ANDROID_HOME` or `sdk.dir` in `local.properties`.

```sh
cd android
./gradlew assembleDebug        # app/build/outputs/apk/debug/app-debug.apk
```

## Install

```sh
adb connect 192.168.1.50:5555  # the TV, over wireless debugging (below)
adb -s 192.168.1.50:5555 install -r app/build/outputs/apk/debug/app-debug.apk
adb -s 192.168.1.50:5555 shell am start -n dev.airwaves.tv/.MainActivity --es server nas:8089
```

Or all of it, from the repo: `scripts/android.sh 192.168.1.50:5555 [nas:8089]`.
The `server` extra is optional: without one, the app asks at first run.

### Wireless debugging on a Google TV

1. Settings > System > About: select **Android TV OS build** seven times, until
   it says you're a developer.
2. Settings > System > Developer options: turn on **Wireless debugging**
   (USB debugging too, if it asks). The TV and the computer must be on the
   same network.
3. In Wireless debugging, choose **Pair device with pairing code**. It shows an
   address with a pairing port and a six-digit code: run
   `adb pair 192.168.1.50:37123` and enter the code. Pairing is once per computer.
4. Connect to the address and port listed under Wireless debugging itself
   (not the pairing port): `adb connect 192.168.1.50:41235`. That port changes
   when the TV restarts; `adb devices` lists what's connected.

Older Android TV (before Android 11) has **Network debugging** instead:
`adb connect 192.168.1.50:5555` and accept the prompt on the TV.

## Using it

- First run, or when the page can't load: setup. Enter the server (`nas:8089`;
  no port means 8089) and Connect; it checks `/api/info` before saving.
- Back goes to the page (`window.airwavesBack()`); when the page has nothing
  to close, Back twice exits. Hold Back, or press Menu, for setup.
- The remote's keys reach the page as keydown events: arrows, Enter (center),
  digits, `MediaPlayPause`, `MediaPlay`, `MediaPause`, `MediaFastForward`,
  `MediaRewind`, `MediaTrackNext`, `MediaTrackPrevious`, `MediaStop`,
  `MediaRecord`, `ChannelUp`, `ChannelDown`, `MediaLast`, `Guide`, `Info`,
  `ClosedCaptionToggle`, `ColorF0Red` and the other colors. The app keeps these
  keys even when the page ignores them.
- In the background the page is hidden (`visibilitychange`) and what's playing
  pauses; it resumes on return. A page can take that over with
  `window.airwavesPause()` and `window.airwavesResume()`. If the WebView's
  renderer dies, the page reloads.

The page talks to the app through `window.AirwavesAndroid`: `getServer()`,
`setServer(url)` (saves it and loads its TV page; `""` opens setup),
`openSetup()`, `getVersion()`, `copyText(text)`, `openUrl(url)` (false when no
app opens it, as for web links on a Google TV, which has no browser),
`log(level, msg)` (logcat tag `Airwaves`) and `exit()`. The user agent ends in
` AirwavesTV/<version>`.

Debug builds can be inspected from Chrome's `chrome://inspect` on the computer.

## Art

The banner, the launcher icon and the setup wordmark are rendered from the HTML
in `art/` (the Steam art's look and the desktop icon's W):
`PLAYWRIGHT=/path/to/node_modules/playwright/index.mjs node android/art/render.mjs`.
