# Builds alertmanager2mackerel and fakemackerel from the source for compose.yml.
# The release image is built by GoReleaser with the Dockerfile in the repository root.
FROM golang:1.27@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/ ./cmd/alertmanager2mackerel ./e2e/fakemackerel

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3 AS alertmanager2mackerel
COPY --from=build /out/alertmanager2mackerel /usr/local/bin/alertmanager2mackerel
ENTRYPOINT ["/usr/local/bin/alertmanager2mackerel"]

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3 AS fakemackerel
COPY --from=build /out/fakemackerel /usr/local/bin/fakemackerel
ENTRYPOINT ["/usr/local/bin/fakemackerel"]
