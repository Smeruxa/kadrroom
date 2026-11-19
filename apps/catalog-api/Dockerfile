FROM golang:1.26-bookworm AS build

WORKDIR /source
COPY go.* ./
COPY apps/catalog-api apps/catalog-api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /catalog-api ./apps/catalog-api

FROM scratch

COPY --from=build /catalog-api /catalog-api
USER 65532:65532
ENV KADRROOM_ADDR=0.0.0.0:8080
EXPOSE 8080

ENTRYPOINT ["/catalog-api"]
