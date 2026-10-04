# airwavesd built from this repository, for running Airwaves on any Docker
# host, amd64 or arm64 (docs/self-hosting.md). Build it from the repository
# root, as compose.yml here does:
#   docker build -f deploy/standalone/Dockerfile -t airwavesd .
# Go cross-compiles on the builder's own platform, so a multi-platform
# build (docker buildx build --platform linux/amd64,linux/arm64 ...) only
# emulates the package installs.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
COPY frontend/embed.go ./frontend/
COPY frontend/dist ./frontend/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/airwavesd ./cmd/airwavesd

# The runtime is deploy/Dockerfile's. Chromium renders the Airwaves Weather
# display to video for the weather channel's HDHomeRun stream. python3 runs
# yt-dlp for YouTube channels (airwavesd keeps the latest release in
# /data/tools), and deno solves YouTube's JavaScript challenges for it.
FROM alpine:3
RUN apk add --no-cache ffmpeg ca-certificates tzdata chromium font-dejavu python3 deno
COPY --from=build /out/airwavesd /usr/local/bin/airwavesd
ENV AIRWAVES_DATA=/data HOME=/tmp
EXPOSE 8089 5004
# Not root; compose.yml picks the user and group (PUID and PGID) that own
# the data and channels folders.
USER 1000:1000
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s CMD ["/usr/local/bin/airwavesd", "-healthcheck"]
ENTRYPOINT ["/usr/local/bin/airwavesd"]
