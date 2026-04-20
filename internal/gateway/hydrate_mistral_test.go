package gateway

import (
	"log/slog"
	"os"
	"testing"
)

func TestLoadMistral(t *testing.T) {
	reg := NewRegistry()
	logger := slog.Default()

	os.Setenv("HELMDECK_MISTRAL_API_KEY", "sk-fake-mistral-key")
	defer os.Unsetenv("HELMDECK_MISTRAL_API_KEY")

	loadMistral(reg, logger)

	provider, ok := reg.Get("mistral") 
	if !ok { 
		t.Fatalf("expected mistral provider to be registered, but it was not found in the registry")
	}

	if provider.Name() != "mistral" {
		t.Errorf("expected provider name 'mistral', got %q", provider.Name())
	}
}