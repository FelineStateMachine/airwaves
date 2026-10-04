#!/bin/sh
# Build the Airwaves Android TV app (debug) and install and start it on a TV.
#   scripts/android.sh 192.168.1.50:5555 [server]
# The device is an adb serial: a TV's wireless debugging address (connected
# first if adb isn't already; pair it once as android/README.md says) or any
# serial from `adb devices`. With server (nas:8089, http://192.168.1.20:8089)
# the app is pointed at it; without, it keeps its setting or asks at first run.
# Builds the working tree. Needs a JDK 17 or later and the Android SDK
# (ANDROID_HOME, or sdk.dir in android/local.properties); both are looked
# for in the usual places when unset.
set -eu
usage='usage: android.sh <device> [server]'
device=${1:?$usage}
server=${2:-}
root=$(cd "$(dirname "$0")/.." && pwd)
app=dev.airwaves.tv
apk=$root/android/app/build/outputs/apk/debug/app-debug.apk

if [ -z "${ANDROID_HOME:-}" ] && [ ! -f "$root/android/local.properties" ]; then
  for sdk in "${ANDROID_SDK_ROOT:-}" "$HOME/Library/Android/sdk" "$HOME/Android/Sdk" \
    /opt/homebrew/share/android-commandlinetools /usr/local/share/android-commandlinetools; do
    if [ -n "$sdk" ] && [ -d "$sdk/platforms" ]; then
      ANDROID_HOME=$sdk
      export ANDROID_HOME
      break
    fi
  done
fi
if [ -z "${JAVA_HOME:-}" ] && ! java -version >/dev/null 2>&1; then
  for jdk in /opt/homebrew/opt/openjdk@21 /opt/homebrew/opt/openjdk@17 /opt/homebrew/opt/openjdk \
    /usr/lib/jvm/java-21-openjdk* /usr/lib/jvm/java-17-openjdk*; do
    if [ -x "$jdk/bin/java" ]; then
      JAVA_HOME=$jdk
      export JAVA_HOME
      break
    fi
  done
fi
adb=adb
command -v adb >/dev/null 2>&1 || adb=${ANDROID_HOME:?no adb: set ANDROID_HOME}/platform-tools/adb

echo "building $(sed -n 's/.*versionName = "\(.*\)"/\1/p' "$root/android/app/build.gradle.kts") at $(git -C "$root" log -1 --format='%h %s')"
(cd "$root/android" && ./gradlew --quiet assembleDebug)

case $device in
  *:*) "$adb" devices | awk -v d="$device" '$1 == d && $2 == "device" { up = 1 } END { exit !up }' ||
    "$adb" connect "$device" ;;
esac
"$adb" -s "$device" install -r "$apk"
if [ -n "$server" ]; then
  "$adb" -s "$device" shell am start -n "$app/.MainActivity" --es server "'$server'"
else
  "$adb" -s "$device" shell am start -n "$app/.MainActivity"
fi
