# Helmdeck control plane image. Multi-stage so the runtime image is the
# bare static binary on distroless/static — see ADR 002.
#
# Built locally by `docker compose build` or by a future GH Actions job
# that publishes ghcr.io/tosin2013/helmdeck:vX.Y.Z. The Compose tier
# (deploy/compose/compose.yaml) builds from this file at the repo root
# context.

FROM golang:1.26-alpine AS build
WORKDIR /src
ARG VERSION=dev
ARG COMMIT=unknown

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
# web/embed.go is imported by internal/api/web.go to serve the
# Management UI bundle via go:embed. Must be present in the build
# context even if web/dist/ contains only the placeholder index.html
# (the embed compiles fine against the placeholder; the real bundle
# lands at make web-build time on the host or via a separate
# multi-stage when CI builds the image).
COPY web ./web

RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/control-plane \
      ./cmd/control-plane

# Pre-create /data with nonroot (UID 65532) ownership so a fresh Docker
# named volume inherits it on first mount. Without this, distroless's
# nonroot user gets EACCES on the SQLite open and the control plane
# crash-loops with "unable to open database file: out of memory (14)".
RUN mkdir -p /out/data && chown -R 65532:65532 /out/data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/control-plane /usr/local/bin/control-plane
COPY --from=build --chown=nonroot:nonroot /out/data /data
USER nonroot:nonroot
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/control-plane"]
