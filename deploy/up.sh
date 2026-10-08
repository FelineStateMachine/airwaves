#!/bin/sh
# Start or update the Airwaves server.
set -eu
cd "$(dirname "$0")"
# shellcheck disable=SC1091
[ -f .env ] && . ./.env
mkdir -p data "${RECORDINGS:-recordings}" "${MUSIC:-music}" "${CHANNELS:-channels}"
docker compose up -d --build --remove-orphans
