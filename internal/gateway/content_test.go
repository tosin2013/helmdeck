package gateway

import (
	"encoding/json"
	"testing"
)

func TestMessageContentDecodesString(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"role":"user","content":"hello"}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content.IsMultipart() {
		t.Error("expected text-only content")
	}
	if m.Content.Text() != "hello" {
		t.Errorf("text = %q", m.Content.Text())
	}
}

func TestMessageContentDecodesArray(t *testing.T) {
	body := `{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}`
	var m Message
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	if !m.Content.IsMultipart() {
		t.Fatal("expected multipart content")
	}
	parts := m.Content.Parts()
	if len(parts) != 2 || parts[0].Type != "text" || parts[1].Type != "image_url" {
		t.Errorf("parts wrong: %+v", parts)
	}
	if imgs := m.Content.Images(); len(imgs) != 1 || imgs[0].URL != "https://x/y.png" {
		t.Errorf("images wrong: %+v", imgs)
	}
	if m.Content.Text() != "hi" {
		t.Errorf("text projection wrong: %q", m.Content.Text())
	}
}

func TestMessageContentMarshalRoundTrip(t *testing.T) {
	cases := []MessageContent{
		TextContent("hello"),
		MultipartContent(TextPart("hi"), ImageURLPartFromURL("https://x")),
	}
	for _, want := range cases {
		raw, err := json.Marshal(Message{Role: "user", Content: want})
		if err != nil {
			t.Fatal(err)
		}
		var got Message
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Content.IsMultipart() != want.IsMultipart() {
			t.Errorf("multipart drift: want=%v got=%v raw=%s", want.IsMultipart(), got.Content.IsMultipart(), raw)
		}
		if got.Content.Text() != want.Text() {
			t.Errorf("text drift: want=%q got=%q raw=%s", want.Text(), got.Content.Text(), raw)
		}
	}
}

func TestMessageContentRejectsBadShape(t *testing.T) {
	var m Message
	err := json.Unmarshal([]byte(`{"role":"user","content":42}`), &m)
	if err == nil {
		t.Fatal("expected error for non-string non-array content")
	}
}
