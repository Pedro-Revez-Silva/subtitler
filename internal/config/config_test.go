package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadExpandsEnvAndParsesDurations(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
openai:
  api_key: ${OPENAI_API_KEY}
subtitles:
  required_languages: [en, pt-PT]
processing:
  scan_interval: 15m
  retry_failed_after: 2h
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpenAI.APIKey != "test-key" {
		t.Fatalf("expected env-expanded api key, got %q", cfg.OpenAI.APIKey)
	}
	if cfg.Processing.ScanInterval != 15*time.Minute {
		t.Fatalf("expected 15m scan interval, got %s", cfg.Processing.ScanInterval)
	}
	if cfg.Processing.RetryFailedAfter != 2*time.Hour {
		t.Fatalf("expected 2h retry delay, got %s", cfg.Processing.RetryFailedAfter)
	}
}
