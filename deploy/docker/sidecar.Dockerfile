# Helmdeck browser sidecar — see ADR 001, ADR 011, T104.
#
# Single image, two runtime modes selected by the entrypoint:
#   headless (default) — Chromium with --remote-debugging-port=9222
#   desktop  (DISPLAY=:99) — Xvfb + XFCE4 + noVNC for vision packs and operator viewing
#
# Layer ordering: cheap things first (base + fonts + locale), then Chromium,
# then desktop stack, then pack dependencies. This keeps `docker build` cache
# hits high when the only thing that changes is a pack-tool addition.
#
# See docs/SIDECAR-EXTENDING.md for the operator runbook on adding tools,
# fonts, and language packs.

FROM debian:bookworm-slim AS marp-dl
ARG MARP_VERSION=4.0.4
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl tar \
 && curl -fsSL "https://github.com/marp-team/marp-cli/releases/download/v${MARP_VERSION}/marp-cli-v${MARP_VERSION}-linux.tar.gz" -o /tmp/marp.tgz \
 && tar -xzf /tmp/marp.tgz -C /usr/local/bin marp \
 && chmod +x /usr/local/bin/marp \
 && /usr/local/bin/marp --version

FROM debian:bookworm-slim
ARG DEBIAN_FRONTEND=noninteractive

# Layer 1 — base, locale, fonts, CA bundle
#
# Latin + emoji only by default. CJK and other regional language packs are
# added downstream per docs/SIDECAR-EXTENDING.md to keep the upstream image
# under the 1.8 GB soft cap.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      dumb-init \
      locales \
      fonts-liberation \
      fonts-noto-core \
      fonts-noto-color-emoji \
      tzdata \
 && sed -i 's/^# *\(en_US.UTF-8\)/\1/' /etc/locale.gen && locale-gen \
 && rm -rf /var/lib/apt/lists/*
ENV LANG=en_US.UTF-8 LANGUAGE=en_US:en LC_ALL=en_US.UTF-8 TZ=UTC

# Layer 2 — Chromium and the bits Chromium needs to actually start in a container
RUN apt-get update && apt-get install -y --no-install-recommends \
      chromium \
      chromium-driver \
      libnss3 \
      libxss1 \
      libasound2 \
      libxshmfence1 \
      libgbm1 \
      libgtk-3-0 \
      libdrm2 \
      libxkbcommon0 \
      libxcomposite1 \
      libxdamage1 \
      libxrandr2 \
      libxfixes3 \
      libxcursor1 \
      libxi6 \
      libxtst6 \
      libpango-1.0-0 \
      libcairo2 \
      libcups2 \
 && rm -rf /var/lib/apt/lists/*

# Layer 3 — desktop stack (Xvfb + minimal XFCE4 components + noVNC + websockify)
#
# xfce4 (meta-package) pulls ~600MB of optional applets, themes, and
# accessories that an agent never touches. We install just enough of XFCE
# to give vision packs a real window manager and panel.
RUN apt-get update && apt-get install -y --no-install-recommends \
      xvfb \
      xfce4-session \
      xfce4-panel \
      xfwm4 \
      xfdesktop4 \
      dbus-x11 \
      novnc \
      websockify \
      x11vnc \
      x11-utils \
      x11-xserver-utils \
 && rm -rf /var/lib/apt/lists/*

# Layer 4 — pack dependencies (Marp from build stage, plus tesseract / ffmpeg / xdotool / scrot / xclip / socat)
#
# socat is the workaround for Chromium 122+ ignoring --remote-debugging-address.
# Modern Chromium hardcodes the CDP listener to 127.0.0.1; we run socat alongside
# to expose port 9222 on the container's eth0 interface for the control plane to
# reach. Bound to $(hostname -i) so it doesn't collide with Chromium on lo:9222.
COPY --from=marp-dl /usr/local/bin/marp /usr/local/bin/marp
RUN apt-get update && apt-get install -y --no-install-recommends \
      tesseract-ocr \
      tesseract-ocr-eng \
      ffmpeg \
      xdotool \
      scrot \
      xclip \
      imagemagick \
      poppler-utils \
      socat \
      iproute2 \
      git \
      openssh-client \
      python3 \
      universal-ctags \
 && rm -rf /var/lib/apt/lists/*

# Layer 4b — Node.js 20 + @playwright/mcp (T807a / ADR 035)
#
# Playwright MCP is the "don't rebuild the browser automation layer" half
# of ADR 035: it exposes Chromium via the accessibility tree so weak LLMs
# can drive the browser without CSS selectors or a vision model. We install
# it globally into the image (one-time cost) and the entrypoint points it
# at the *existing* Chromium over CDP (--cdp-endpoint http://127.0.0.1:9222),
# so there is no second Chromium process — both chromedp-based packs and
# Playwright MCP share a single browser.
#
# PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 is critical: without it the Playwright
# postinstall would pull ~200 MB of bundled Chromium that we would never
# use. The image already has the system chromium from Layer 2.
#
# The SSE/HTTP surface is bound to 0.0.0.0:8931 at runtime by the
# entrypoint, so exposing it here is just a hint for `docker inspect`.
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
 && apt-get update && apt-get install -y --no-install-recommends nodejs \
 && PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm install -g @playwright/mcp@latest \
 && npm cache clean --force \
 && rm -rf /var/lib/apt/lists/* /root/.npm

# Layer 5 — non-root user, runtime dirs, entrypoint
RUN groupadd --system --gid 1000 helmdeck \
 && useradd  --system --uid 1000 --gid helmdeck --shell /bin/bash --create-home helmdeck \
 && mkdir -p /home/helmdeck/.config/chromium /home/helmdeck/artifacts /var/run/dbus \
 && chown -R helmdeck:helmdeck /home/helmdeck

COPY deploy/docker/sidecar-entrypoint.sh /usr/local/bin/helmdeck-entrypoint
RUN chmod +x /usr/local/bin/helmdeck-entrypoint

USER helmdeck
WORKDIR /home/helmdeck

ENV CHROMIUM_PORT=9222 \
    PLAYWRIGHT_MCP_PORT=8931 \
    HELMDECK_MODE=headless \
    HELMDECK_PLAYWRIGHT_MCP_ENABLED=true \
    HOME=/home/helmdeck

EXPOSE 9222 6080 8931

# dumb-init reaps zombies cleanly so the watchdog/runtime get correct exit codes.
ENTRYPOINT ["/usr/bin/dumb-init", "--", "/usr/local/bin/helmdeck-entrypoint"]
