# Builds alertmanager2mackerel and fakemackerel from the source for compose.yml.
# The release image is built by GoReleaser with the Dockerfile in the repository root.
FROM golang:1.27@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/ ./cmd/alertmanager2mackerel ./e2e/fakemackerel

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS alertmanager2mackerel
COPY --from=build /out/alertmanager2mackerel /usr/local/bin/alertmanager2mackerel
ENTRYPOINT ["/usr/local/bin/alertmanager2mackerel"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS fakemackerel
COPY --from=build /out/fakemackerel /usr/local/bin/fakemackerel
ENTRYPOINT ["/usr/local/bin/fakemackerel"]
