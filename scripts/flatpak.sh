#!/bin/sh
# Build the Airwaves desktop app as a Flatpak and install it on a SteamOS
# (or any Flatpak) box for its user.
#   scripts/flatpak.sh nas deck@steamdeck [server]
# The app is built from the committed HEAD (uncommitted changes are left
# out; the packaging in build/linux/flatpak comes from the working tree) on
# a Docker host, in ~/airwaves-flatpak with the flathub-infra GNOME image.
# INCLUDE_WORKTREE adds uncommitted changes: paths to take from the working
# tree ("frontend/dist main.go"), or 1 for every changed tracked file.
# A first install points the app at server (default: the build host's
# tailnet address) and adds it to Steam as a non-Steam game.
set -eu
usage='usage: flatpak.sh <build-host> <deck-host> [server]'
builder=${1:?$usage}
deck=${2:?$usage}
root=$(cd "$(dirname "$0")/.." && pwd)
app=dev.airwaves.Airwaves
gnome=$(sed -n "s/^runtime-version: *'*\([0-9.]*\)'*/\1/p" "$root/build/linux/flatpak/$app.yml")
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

echo "building $(git -C "$root" log -1 --format='%h %s') on GNOME $gnome"
mkdir -p "$stage/src"
git -C "$root" archive HEAD | tar -x -C "$stage/src"
if [ -n "${INCLUDE_WORKTREE:-}" ]; then
  paths=$INCLUDE_WORKTREE
  [ "$paths" = 1 ] && paths=$(git -C "$root" diff --name-only --diff-filter=d HEAD)
  echo "with uncommitted:" $paths
  # shellcheck disable=SC2086 # a list of paths
  (cd "$root" && tar -cf - $paths) | tar -x -C "$stage/src"
fi
rm -rf "$stage/src/build/linux/flatpak"
mkdir -p "$stage/src/build/linux"
cp -R "$root/build/linux/flatpak" "$stage/src/build/linux/"

# Runs in the image as root, in ~/airwaves-flatpak. The Go SDK extension
# stays installed in ./flatpak between builds; the GNOME SDK is in the image.
cat > "$stage/build.sh" <<'EOF'
#!/bin/sh
set -eu
app=dev.airwaves.Airwaves
# Files are root's while building (flatpak insists on that for ./flatpak)
# and the host user's afterwards.
trap 'chown -R "$OWNER" /work' EXIT
export FLATPAK_USER_DIR=/work/flatpak
mkdir -p "$FLATPAK_USER_DIR"
chown -R 0:0 "$FLATPAK_USER_DIR"
flatpak remote-add --user --if-not-exists flathub https://dl.flathub.org/repo/flathub.flatpakrepo
sdk=$(flatpak info -m "org.gnome.Sdk//$GNOME" |
  sed -n '/^\[Extension org.freedesktop.Sdk.Extension\]/,/^\[/s/^version *= *//p')
flatpak install --user -y --noninteractive --or-update flathub "org.freedesktop.Sdk.Extension.golang//$sdk"
flatpak-builder --disable-rofiles-fuse --force-clean --state-dir=state --repo=repo \
  build "src/build/linux/flatpak/$app.yml"
flatpak build-update-repo --prune --prune-depth=1 repo >/dev/null
flatpak build-bundle --runtime-repo=https://dl.flathub.org/repo/flathub.flatpakrepo \
  repo "$app.flatpak" "$app"
ls -lh "$app.flatpak"
EOF

ssh "$builder" 'mkdir -p ~/airwaves-flatpak'
rsync -a --delete "$stage/src/" "$builder:airwaves-flatpak/src/"
rsync -a "$stage/build.sh" "$builder:airwaves-flatpak/"
ssh "$builder" "cd ~/airwaves-flatpak && docker run --rm --privileged \
  -v \"\$PWD:/work\" -w /work -e OWNER=\$(id -u):\$(id -g) -e GNOME=$gnome \
  ghcr.io/flathub-infra/flatpak-github-actions:gnome-$gnome sh build.sh"
server=${3:-$(ssh "$builder" 'tailscale ip -4' | head -1)}

scp -q "$builder:airwaves-flatpak/$app.flatpak" "$stage/"
scp -q "$stage/$app.flatpak" "$deck:/tmp/$app.flatpak"
ssh "$deck" sh -s -- "$gnome" "$server" <<'EOF'
set -eu
app=dev.airwaves.Airwaves
gnome=$1 server=$2
bundle=/tmp/$app.flatpak
trap 'rm -f "$bundle"' EXIT
flatpak remote-add --user --if-not-exists flathub https://dl.flathub.org/repo/flathub.flatpakrepo
flatpak install --user -y --noninteractive --or-update flathub "org.gnome.Platform//$gnome"
# H.264 and AAC for WebKitGTK's GStreamer come from codecs-extra.
codecs=$(flatpak info --user -m "org.gnome.Platform//$gnome" |
  sed -n '/^\[Extension org.freedesktop.Platform.codecs-extra\]/,/^\[/s/^version *= *//p')
flatpak install --user -y --noninteractive --or-update flathub "org.freedesktop.Platform.codecs-extra//$codecs"
first=
flatpak info --user "$app" >/dev/null 2>&1 || first=1
flatpak install --user -y --noninteractive --reinstall --bundle "$bundle"
flatpak info --user "$app" | sed -n 's/^ *\(Version\|Commit\|Installed\): */  \1: /p'

cfg=$HOME/.var/app/$app/config/airwaves/settings.json
if [ ! -f "$cfg" ]; then
  mkdir -p "$(dirname "$cfg")"
  printf '{\n  "server": "%s"\n}\n' "$server" > "$cfg"
  echo "wrote $cfg (server $server)"
fi
if [ -n "$first" ]; then
  if steamos-add-to-steam "$HOME/.local/share/flatpak/exports/share/applications/$app.desktop"; then
    echo "added to Steam"
  else
    echo "could not add to Steam; add it from Desktop Mode" >&2
  fi
fi
EOF
