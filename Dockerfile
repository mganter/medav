# GoReleaser builds the static binary and supplies it in the build context; this
# Dockerfile only assembles the final image. The distroless static:nonroot base
# ships no shell or package manager and runs as an unprivileged user (uid 65532).
#
# Supply-chain note: the "nonroot" tag is mutable, so builds are not
# reproducible. For stronger integrity pin by immutable digest and bump via
# Dependabot/Renovate:
#   FROM gcr.io/distroless/static:nonroot@sha256:<digest>
FROM gcr.io/distroless/static:nonroot

# GoReleaser (dockers_v2) lays the prebuilt binaries out under <os>/<arch>/ and
# buildx supplies TARGETPLATFORM (e.g. "linux/amd64") for the current target.
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/medav /usr/bin/medav

# Runs as the non-privileged "nonroot" user, so bind to an unprivileged port.
USER nonroot:nonroot
EXPOSE 8080

# Containers reach medav over the container network, so it must listen on the
# container interface rather than the loopback default. Because that exposes the
# unauthenticated server to whatever can route to the container, set
# PROXY_AUTH_SECRET (and inject the matching header from your proxy) or keep
# medav on a private network only the proxy can reach. See README "Security".
ENV LISTEN_ADDR=":8080"

ENTRYPOINT ["/usr/bin/medav"]
