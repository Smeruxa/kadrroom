FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /source
COPY go.* ./
RUN go mod download
COPY apps/identity-api apps/identity-api
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w" -o /identity-api ./apps/identity-api

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /identity-api /identity-api
USER 65532:65532
ENV KADRROOM_IDENTITY_ADDR=0.0.0.0:8081
EXPOSE 8081
STOPSIGNAL SIGTERM

ENTRYPOINT ["/identity-api"]
