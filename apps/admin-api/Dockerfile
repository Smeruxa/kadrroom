FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /source
COPY go.* ./
COPY internal/catalogmedia internal/catalogmedia
COPY apps/admin-api apps/admin-api
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w" -o /admin-api ./apps/admin-api \
    && mkdir -p /media

FROM scratch

COPY --from=build /admin-api /admin-api
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /media /var/lib/kadrroom/media
USER 65532:65532
ENV KADRROOM_ADMIN_ADDR=0.0.0.0:8083
EXPOSE 8083
STOPSIGNAL SIGTERM

ENTRYPOINT ["/admin-api"]
