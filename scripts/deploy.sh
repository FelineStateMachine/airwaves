#!/bin/sh
# Deploy the Airwaves server to a Docker host over SSH.
#   AIRWAVES_ZIP=12345 scripts/deploy.sh nas
# Creates ~/airwaves on the host; .env there holds host-specific settings,
# written on the first deploy: the name, the tailnet address to listen on,
# the host's time zone and the antenna's ZIP code (AIRWAVES_ZIP).
set -eu
host=${1:?usage: deploy.sh <ssh-host>}
root=$(cd "$(dirname "$0")/.." && pwd)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

mkdir -p "$stage/airwavesd"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -C "$root" -trimpath -ldflags="-s -w" -o "$stage/airwavesd/airwavesd" ./cmd/airwavesd
cp "$root/deploy/Dockerfile" "$stage/airwavesd/"
cp "$root/deploy/compose.yml" "$root/deploy/up.sh" "$stage/"
cp -R "$root/deploy/weatherstar" "$stage/"

ssh "$host" 'mkdir -p ~/airwaves'
rsync -a "$stage/" "$host:airwaves/"
ssh "$host" "ZIP='${AIRWAVES_ZIP:-}' sh -s" <<'EOF'
set -eu
cd ~/airwaves
if [ ! -f .env ]; then
  ip=$(tailscale ip -4 2>/dev/null | head -1)
  tz=$(readlink /etc/localtime 2>/dev/null | sed 's|.*/zoneinfo/||')
  printf "AIRWAVES_NAME=%s\nAIRWAVES_BIND=%s\nTZ=%s\nAIRWAVES_ZIP=%s\n" "$(hostname)" "${ip:-127.0.0.1}" "${tz:-UTC}" "$ZIP" > .env
  echo "wrote .env:"; cat .env
fi
for key in TZ AIRWAVES_ZIP; do
  if ! grep -q "^$key=." .env; then
    echo "~/airwaves/.env needs $key (the time zone, like America/Chicago; the antenna's ZIP code)" >&2
    exit 1
  fi
done
./up.sh
EOF
