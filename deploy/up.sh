#!/bin/sh
# Start or update the Airwaves server. Run again after plugging in a tuner.
set -eu
cd "$(dirname "$0")"
# shellcheck disable=SC1091
[ -f .env ] && . ./.env
mkdir -p data tvheadend/config "${RECORDINGS:-recordings}" "${MUSIC:-music}" "${CHANNELS:-channels}"
files="-f compose.yml"
if [ -d /dev/dvb ]; then
  files="$files -f compose.dvb.yml"
  echo "tuner found: $(ls /dev/dvb | tr '\n' ' ')"
fi
docker compose $files up -d --build --remove-orphans
