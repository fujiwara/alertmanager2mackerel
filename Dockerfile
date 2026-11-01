FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/alertmanager2mackerel /usr/local/bin/alertmanager2mackerel
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/alertmanager2mackerel"]
