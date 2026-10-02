FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/alertmanager2mackerel /usr/local/bin/alertmanager2mackerel
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/alertmanager2mackerel"]
