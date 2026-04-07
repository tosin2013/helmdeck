# Distroless image for the helmdeck-mcp stdio bridge.
# Built by goreleaser; the helmdeck-mcp binary is supplied by the build context.
# See ADR 030.
FROM gcr.io/distroless/static:nonroot
COPY helmdeck-mcp /usr/local/bin/helmdeck-mcp
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/helmdeck-mcp"]
