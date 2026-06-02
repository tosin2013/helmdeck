# Helmdeck control plane image. Multi-stage so the runtime image is the
# bare static binary on distroless/static — see ADR 002.
#
# Two build stages produce the final image:
#   1. web-build   — Node runs `npm ci && npm run build` to produce
#                    web/dist/{index.html,assets/*} with content-hashed
#                    bundles. Self-contained: the host's web/dist is
#                    .dockerignore'd out of the build context, so this
#                    stage cannot pick up stale local bundles.
#   2. build       — Go compiles the control-plane binary with
#                    //go:embed all:dist (web/embed.go) pulling the
#                    bundle the web-build stage produced.
#
# Self-consistent by construction: there is no path in this Dockerfile
# that lets the embedded HTML reference asset hashes the image doesn't
# also contain. That class of "page loads HTML but blanks on hash
# mismatch" bug is impossible against an image built from this file.

FROM node:20-alpine AS web-build
WORKDIR /web

# Stage dependencies first so changes to src/ don't bust the npm cache.
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund --prefer-offline

# Then the source. .dockerignore keeps web/dist and web/node_modules
# out of the build context, so this COPY only brings source files.
COPY web ./
RUN npm run build

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
# Management UI bundle via go:embed. The host's web/dist is .dockerignore'd
# (it is a build artifact, not source — see fix/dockerfile-web-build-stage
# rationale in CHANGELOG), so we COPY the web/ source files here for the
# Go embed pattern matchers + embed.go itself, then overlay the freshly
# built dist from the web-build stage. The embed compiles against the
# Node-produced bundle, not whatever happened to be on the host.
COPY web ./web
COPY --from=web-build /web/dist ./web/dist

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
