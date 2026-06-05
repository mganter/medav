# GoReleaser builds the static binary and supplies it in the build context; this
# Dockerfile only assembles the final image. The distroless static-nonroot base
# ships no shell or package manager and runs as an unprivileged user (uid 65532).
FROM gcr.io/distroless/static-nonroot:latest

# GoReleaser (dockers_v2) lays the prebuilt binaries out under <os>/<arch>/ and
# buildx supplies TARGETPLATFORM (e.g. "linux/amd64") for the current target.
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/medav /usr/bin/medav

# Runs as the non-privileged "nonroot" user, so bind to an unprivileged port.
USER nonroot:nonroot
EXPOSE 8080

ENV LISTEN_ADDR=":8080"

ENTRYPOINT ["/usr/bin/medav"]
