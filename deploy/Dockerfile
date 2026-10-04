# Runtime image for airwavesd. The binary is cross-compiled by
# scripts/deploy.sh and copied in next to this file.
FROM alpine:3
# Chromium renders the Airwaves Weather display to video for the custom
# weather channel. python3 runs yt-dlp for YouTube channels (airwavesd keeps
# the latest release in /data/tools), and deno solves YouTube's JavaScript
# challenges for it.
RUN apk add --no-cache ffmpeg ca-certificates tzdata chromium font-dejavu python3 deno
COPY airwavesd /usr/local/bin/airwavesd
ENV AIRWAVES_DATA=/data HOME=/tmp
EXPOSE 8089
USER 1000:100
ENTRYPOINT ["/usr/local/bin/airwavesd"]
