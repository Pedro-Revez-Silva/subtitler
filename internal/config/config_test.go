package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadExpandsEnvAndParsesDurations(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("SENTRY_DSN", "https://example.invalid/1")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
openai:
  api_key: ${OPENAI_API_KEY}
telemetry:
  enabled: true
  sentry_dsn: ${SENTRY_DSN}
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
	if !cfg.Telemetry.EnabledValue() || cfg.Telemetry.SentryDSN != "https://example.invalid/1" {
		t.Fatalf("expected env-expanded telemetry config, got %#v", cfg.Telemetry)
	}
	if cfg.Processing.ScanInterval != 15*time.Minute {
		t.Fatalf("expected 15m scan interval, got %s", cfg.Processing.ScanInterval)
	}
	if cfg.Processing.RetryFailedAfter != 2*time.Hour {
		t.Fatalf("expected 2h retry delay, got %s", cfg.Processing.RetryFailedAfter)
	}
}

func TestLoadAppliesDefaultsAndMapsPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
subtitles:
  path_mappings:
    - from: /movies
      to: /media/movies
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StatePath != "subtitler-state.json" || cfg.TempDir == "" {
		t.Fatalf("expected default state/temp paths, got %#v", cfg)
	}
	if cfg.OpenAI.BaseURL != "https://api.openai.com/v1" || cfg.OpenAI.TranscriptionModel != "whisper-1" {
		t.Fatalf("expected OpenAI defaults, got %#v", cfg.OpenAI)
	}
	if cfg.OpenAI.MaxChunkSeconds != 1200 || cfg.OpenAI.MaxChunkBytes != 24_000_000 || cfg.OpenAI.ChunkRetries != 2 {
		t.Fatalf("expected OpenAI chunk defaults, got %#v", cfg.OpenAI)
	}
	if cfg.Subtitles.RequiredLanguages[0] != "en" || cfg.Subtitles.Output.Title != "subtitler" {
		t.Fatalf("expected subtitle defaults, got %#v", cfg.Subtitles)
	}
	if len(cfg.Subtitles.SourceAudioLanguages) != 2 || cfg.Subtitles.SourceAudioLanguages[0] != "en" || cfg.Subtitles.SourceAudioLanguages[1] != "auto" {
		t.Fatalf("expected English-first source audio defaults, got %#v", cfg.Subtitles.SourceAudioLanguages)
	}
	if cfg.Subtitles.SourceSubtitleLanguage != "en" {
		t.Fatalf("expected English source subtitle default, got %q", cfg.Subtitles.SourceSubtitleLanguage)
	}
	if cfg.Processing.ScanInterval != 30*time.Minute || cfg.Processing.Concurrency != 1 || cfg.Processing.MaxAttempts != 3 {
		t.Fatalf("expected processing defaults, got %#v", cfg.Processing)
	}
	if !cfg.Telemetry.EnabledValue() || cfg.Telemetry.SentryDSN != DefaultTelemetrySentryDSN {
		t.Fatalf("expected default telemetry to be enabled with built-in DSN, got %#v", cfg.Telemetry)
	}
	if got := cfg.MapPath("/movies/Film/movie.mkv"); got != "/media/movies/Film/movie.mkv" {
		t.Fatalf("unexpected mapped path %q", got)
	}
	if got := cfg.MapPath("/movies"); got != "/media/movies" {
		t.Fatalf("unexpected exact mapped path %q", got)
	}
	if got := cfg.MapPath("/series/Episode.mkv"); got != "/series/Episode.mkv" {
		t.Fatalf("unexpected unmapped path %q", got)
	}
}

func TestDefaultUsesEnvironmentFallbacks(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-env")
	t.Setenv("SONARR_API_KEY", "sonarr-env")
	t.Setenv("RADARR_API_KEY", "radarr-env")
	t.Setenv("SENTRY_DSN", "https://sentry.example/1")

	cfg := Default()
	if cfg.OpenAI.APIKey != "openai-env" {
		t.Fatalf("expected OpenAI key from env, got %q", cfg.OpenAI.APIKey)
	}
	if cfg.Sonarr.APIKey != "sonarr-env" || cfg.Radarr.APIKey != "radarr-env" {
		t.Fatalf("expected ARR keys from env, got sonarr=%q radarr=%q", cfg.Sonarr.APIKey, cfg.Radarr.APIKey)
	}
	if cfg.Telemetry.SentryDSN != "https://sentry.example/1" {
		t.Fatalf("expected Sentry DSN from env, got %q", cfg.Telemetry.SentryDSN)
	}
}

func TestLoadAllowsTelemetryOptOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
telemetry:
  enabled: false
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telemetry.EnabledValue() {
		t.Fatal("expected configured telemetry opt-out")
	}
}

func TestLoadUsesLegacyAudioLanguageAsSourcePreference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
subtitles:
  audio_language: pt-PT
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Subtitles.SourceAudioLanguages) != 2 || cfg.Subtitles.SourceAudioLanguages[0] != "pt-PT" || cfg.Subtitles.SourceAudioLanguages[1] != "auto" {
		t.Fatalf("expected legacy audio language source preference, got %#v", cfg.Subtitles.SourceAudioLanguages)
	}
}

func TestTelemetryEnvironmentOptOut(t *testing.T) {
	t.Setenv("SUBTITLER_TELEMETRY", "off")

	cfg := Default()
	if cfg.Telemetry.EnabledValue() {
		t.Fatal("expected telemetry env opt-out")
	}
}

func TestLoadEnvironmentFallbackDoesNotOverrideConfiguredValues(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
openai:
  api_key: configured-key
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpenAI.APIKey != "configured-key" {
		t.Fatalf("expected configured key to win, got %q", cfg.OpenAI.APIKey)
	}
}

func TestLoadValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "format", body: "subtitles:\n  output:\n    format: vtt\n"},
		{name: "strategy", body: "subtitles:\n  strategy: invalid\n"},
		{name: "cleanup", body: "subtitles:\n  cleanup:\n    external_subtitles: burn\n"},
		{name: "embedded", body: "subtitles:\n  embedded:\n    action: remove\n"},
		{name: "model", body: "openai:\n  transcription_model: \"   \"\n"},
		{name: "source language", body: "subtitles:\n  source_subtitle_language: \"   \"\n"},
		{name: "source audio empty", body: "subtitles:\n  source_audio_languages: [en, \"\"]\n"},
		{name: "job limit", body: "processing:\n  max_jobs_per_scan: -1\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadReadAndYAMLErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected missing file error")
	}
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("subtitles: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected YAML parse error")
	}
}
