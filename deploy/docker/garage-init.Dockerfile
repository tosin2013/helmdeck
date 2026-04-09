# Garage bootstrap helper image (ADR 031, T211a).
#
# dxflrs/garage is FROM scratch with just the garage binary, so it
# can't run a shell script. This image copies that binary onto an
# alpine base so the bootstrap logic in deploy/compose/garage-init.sh
# can drive `garage` CLI commands with normal POSIX-shell control flow.
#
# Pin the garage version to the same tag the compose stack runs to
# guarantee CLI/server compatibility.

FROM dxflrs/garage:v2.2.0 AS garage
FROM alpine:3.20

RUN apk add --no-cache bash gawk

COPY --from=garage /garage /usr/local/bin/garage

ENTRYPOINT ["/bin/sh"]
