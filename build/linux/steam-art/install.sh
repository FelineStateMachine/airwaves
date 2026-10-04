#!/bin/sh
# Installs the Steam library art for the Airwaves non-Steam shortcut on a SteamOS box.
#
#   PLAYWRIGHT=.../node_modules/playwright/index.mjs node build/linux/steam-art/render.mjs   # re-render PNGs
#   build/linux/steam-art/install.sh user@host                                           # deck@steamdeck
#
# Copies the PNGs into the grid folder of the Steam user whose shortcuts.vdf has Airwaves,
# named by the shortcut's appid. If Steam's CEF remote debugging is on (Decky turns it on),
# it also hands the art to the running Steam client so it shows without a restart, and sets
# the shortcut's icon through Steam's API rather than editing shortcuts.vdf under it.
# NO_LIVE=1 only copies the files.
set -eu
host=${1:?usage: install.sh user@host}
dir=$(cd "$(dirname "$0")" && pwd)

set -- $(ssh "$host" python3 - find < "$dir/steamclient.py")
grid=$1 appid=$2
echo "grid $grid, appid $appid"

ssh "$host" mkdir -p "$grid"
scp -q "$dir/capsule.png" "$host:$grid/${appid}p.png"
scp -q "$dir/header.png" "$host:$grid/${appid}.png"
scp -q "$dir/hero.png" "$host:$grid/${appid}_hero.png"
scp -q "$dir/logo.png" "$host:$grid/${appid}_logo.png"
scp -q "$dir/icon.png" "$host:$grid/${appid}_icon.png"
echo "copied art"

if [ "${NO_LIVE:-}" = "" ] && ssh "$host" curl -sf -o /dev/null http://127.0.0.1:8080/json; then
  ssh "$host" python3 - apply "$grid" "$appid" < "$dir/steamclient.py"
else
  echo "Steam reads the grid folder when it starts, so the art shows after a Steam restart."
  echo "Set the shortcut's icon to $grid/${appid}_icon.png in its properties (or rerun with CEF debugging on)."
fi
