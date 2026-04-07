# 28. WebRTC Live Session Streaming

**Status**: Proposed
**Date**: 2026-04-07
**Domain**: distributed-systems

## Context
The Live Desktop Viewer specified in §13.5 uses noVNC over WebSocket, which adds 200–400 ms of latency, requires a per-session VNC server process, and offers no audio path. For interactive use (operator intervention, OpenAdapt-style demonstration recording, voice-driven sessions), this latency is the difference between "usable" and "unusable" (PRD §19.9).

## Decision
Add a **WebRTC Live View** mode alongside (not replacing) the noVNC viewer.

**Capture path:** subscribe to CDP `Page.startScreencast` and `Page.screencastFrame` events on the session, which deliver JPEG/H.264 frames already encoded by Chromium. For Xvfb-backed desktop sessions, use ffmpeg with `x11grab` → H.264 instead.

**Transport:** the Go control plane implements a lightweight WebRTC signaling endpoint at `POST /api/v1/sessions/{id}/webrtc/offer` using the `pion/webrtc` Go library. The browser (or any WebRTC-capable client) sends an SDP offer; the control plane returns an answer and begins forwarding screencast frames as RTP packets. For multi-viewer deployments, frames flow through a LiveKit SFU instead of point-to-point.

**Audio:** when the session is desktop-mode (Xvfb + XFCE), pulse audio frames are captured and added as a second WebRTC track, enabling voice-driven session interaction and TTS-narrated playback.

**Bidirectional control:** WebRTC data channel carries operator input events (mouse, keyboard) back to the session, executed via xdotool. This enables real-time operator intervention without context-switching out of the UI.

The Session Explorer (§8.3) gains a "WebRTC Live View" tab next to the existing "VNC Viewer" tab; noVNC remains as the fallback for environments where WebRTC is blocked.

## Consequences
**Positive:** sub-100 ms streaming latency unlocks real-time intervention and voice control; no per-session VNC daemon; LiveKit path scales to many concurrent observers without per-session fanout cost.
**Negative:** WebRTC NAT traversal in restrictive enterprise networks requires a TURN server (operationally non-trivial); audio capture adds another moving part to the desktop session image; H.264 encoding consumes session CPU budget — must be included in resource accounting.

## Related PRD Sections
§19.9 WebRTC Live Session Streaming, §13.5 Live Desktop Viewer, §8.3 Browser Sessions Panel
