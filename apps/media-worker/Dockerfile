FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /source
COPY go.* ./
COPY internal/catalogmedia internal/catalogmedia
COPY apps/media-worker apps/media-worker
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w" -o /media-worker ./apps/media-worker

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/* \
    && install -d -o 65532 -g 65532 /var/lib/kadrroom/media
COPY --from=build /media-worker /media-worker

USER 65532:65532
STOPSIGNAL SIGTERM

ENTRYPOINT ["/media-worker"]
