FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /source
COPY go.* ./
COPY internal/catalogmedia internal/catalogmedia
COPY apps/catalog-api apps/catalog-api
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w" -o /catalog-api ./apps/catalog-api \
    && mkdir -p /media

FROM scratch

COPY --from=build /catalog-api /catalog-api
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /media /var/lib/kadrroom/media
USER 65532:65532
ENV KADRROOM_ADDR=0.0.0.0:8080
EXPOSE 8080
STOPSIGNAL SIGTERM

ENTRYPOINT ["/catalog-api"]
