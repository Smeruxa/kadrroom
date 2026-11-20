FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /source
COPY go.* ./
RUN go mod download
COPY apps/rooms-api apps/rooms-api
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w" -o /rooms-api ./apps/rooms-api

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /rooms-api /rooms-api
USER 65532:65532
ENV KADRROOM_ROOMS_ADDR=0.0.0.0:8082
EXPOSE 8082
STOPSIGNAL SIGTERM

ENTRYPOINT ["/rooms-api"]
