// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package avenc

// testfixtures.go — shared test scaffolding for avenc unit tests.
//
// Kept in a non-_test.go file so test fixtures are available to
// downstream packages that want to construct avenc-shaped mocks
// (e.g. an integration harness). The fixtures here are byte-stable
// constants; the mock Executor is in avenc_test.go (test-only).

// FakeMP3 is a minimal-but-valid MPEG-1 Layer III file: the 2-byte
// sync word (0xFF 0xFB) followed by zero padding to comfortably
// exceed MinTTSResponseBytes. Real callers (slides.narrate's
// elevenLabsTTS, podcast/elevenlabs) compare against this in tests
// that mock a successful TTS response — the bytes pass LooksLikeMP3
// AND the size floor in one fixture.
var FakeMP3 = func() []byte {
	b := make([]byte, MinTTSResponseBytes+512) // comfortably above the floor
	b[0] = 0xFF
	b[1] = 0xFB
	return b
}()

// FakeMP4 is a placeholder for downstream tests that need
// non-zero-length "video bytes" without actually constructing a
// valid mp4 container. RequireNonEmptyOutput operates on file SIZE
// in the sandbox, not in-memory bytes, so this is purely for
// tests that mock a `cat /tmp/final.mp4` read.
var FakeMP4 = []byte("avenc-fake-mp4-bytes-for-tests")
