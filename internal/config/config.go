package config

import (
	"errors"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DryRun     bool             `yaml:"dry_run"`
	StatePath  string           `yaml:"state_path"`
	TempDir    string           `yaml:"temp_dir"`
	Sonarr     ServiceConfig    `yaml:"sonarr"`
	Radarr     ServiceConfig    `yaml:"radarr"`
	OpenAI     OpenAIConfig     `yaml:"openai"`
	Subtitles  SubtitleConfig   `yaml:"subtitles"`
	Processing ProcessingConfig `yaml:"processing"`
	Tools      ToolsConfig      `yaml:"tools"`
}

type ServiceConfig struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
}

type OpenAIConfig struct {
	APIKey             string `yaml:"api_key"`
	TranscriptionModel string `yaml:"transcription_model"`
	TranslationModel   string `yaml:"translation_model"`
	BaseURL            string `yaml:"base_url"`
	Context            string `yaml:"context"`
	MaxChunkSeconds    int    `yaml:"max_chunk_seconds"`
}

type SubtitleConfig struct {
	RequiredLanguages []string       `yaml:"required_languages"`
	AudioLanguage     string         `yaml:"audio_language"`
	Strategy          string         `yaml:"strategy"`
	Cleanup           CleanupConfig  `yaml:"cleanup"`
	Output            OutputConfig   `yaml:"output"`
	Embedded          EmbeddedConfig `yaml:"embedded"`
	PathMappings      []PathMapping  `yaml:"path_mappings"`
	ProtectedSuffixes []string       `yaml:"protected_suffixes"`
}

type CleanupConfig struct {
	ExternalSubtitles string `yaml:"external_subtitles"`
}

type OutputConfig struct {
	Format string `yaml:"format"`
	Title  string `yaml:"title"`
}

type EmbeddedConfig struct {
	Action string `yaml:"action"`
	Title  string `yaml:"title"`
}

type PathMapping struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

type ProcessingConfig struct {
	ScanInterval     time.Duration `yaml:"scan_interval"`
	Concurrency      int           `yaml:"concurrency"`
	RetryFailedAfter time.Duration `yaml:"retry_failed_after"`
	MaxAttempts      int           `yaml:"max_attempts"`
}

type ToolsConfig struct {
	FFmpeg  string `yaml:"ffmpeg"`
	FFprobe string `yaml:"ffprobe"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	expanded := os.ExpandEnv(string(data))
	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return Config{}, err
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.StatePath == "" {
		c.StatePath = "subtitler-state.json"
	}
	if c.TempDir == "" {
		c.TempDir = os.TempDir()
	}
	if c.OpenAI.BaseURL == "" {
		c.OpenAI.BaseURL = "https://api.openai.com/v1"
	}
	if c.OpenAI.TranscriptionModel == "" {
		c.OpenAI.TranscriptionModel = "whisper-1"
	}
	if c.OpenAI.TranslationModel == "" {
		c.OpenAI.TranslationModel = "gpt-4o-mini"
	}
	if c.OpenAI.MaxChunkSeconds <= 0 {
		c.OpenAI.MaxChunkSeconds = 1200
	}
	if len(c.Subtitles.RequiredLanguages) == 0 {
		c.Subtitles.RequiredLanguages = []string{"en"}
	}
	if c.Subtitles.AudioLanguage == "" {
		c.Subtitles.AudioLanguage = "auto"
	}
	if c.Subtitles.Strategy == "" {
		c.Subtitles.Strategy = "missing_only"
	}
	if c.Subtitles.Cleanup.ExternalSubtitles == "" {
		c.Subtitles.Cleanup.ExternalSubtitles = "keep"
	}
	if c.Subtitles.Output.Format == "" {
		c.Subtitles.Output.Format = "srt"
	}
	if c.Subtitles.Output.Title == "" {
		c.Subtitles.Output.Title = "subtitler"
	}
	if c.Subtitles.Embedded.Action == "" {
		c.Subtitles.Embedded.Action = "ignore"
	}
	if c.Subtitles.Embedded.Title == "" {
		c.Subtitles.Embedded.Title = "official"
	}
	if len(c.Subtitles.ProtectedSuffixes) == 0 {
		c.Subtitles.ProtectedSuffixes = []string{"forced"}
	}
	if c.Processing.ScanInterval <= 0 {
		c.Processing.ScanInterval = 30 * time.Minute
	}
	if c.Processing.Concurrency <= 0 {
		c.Processing.Concurrency = 1
	}
	if c.Processing.RetryFailedAfter <= 0 {
		c.Processing.RetryFailedAfter = 24 * time.Hour
	}
	if c.Processing.MaxAttempts <= 0 {
		c.Processing.MaxAttempts = 3
	}
	if c.Tools.FFmpeg == "" {
		c.Tools.FFmpeg = "ffmpeg"
	}
	if c.Tools.FFprobe == "" {
		c.Tools.FFprobe = "ffprobe"
	}
}

func (c Config) validate() error {
	if c.Subtitles.Output.Format != "srt" {
		return errors.New("only srt output is supported right now")
	}
	switch c.Subtitles.Strategy {
	case "missing_only", "generated_only", "force":
	default:
		return errors.New("subtitles.strategy must be one of missing_only, generated_only, force")
	}
	switch c.Subtitles.Cleanup.ExternalSubtitles {
	case "keep", "quarantine", "delete":
	default:
		return errors.New("subtitles.cleanup.external_subtitles must be one of keep, quarantine, delete")
	}
	switch c.Subtitles.Embedded.Action {
	case "ignore", "extract":
	default:
		return errors.New("subtitles.embedded.action must be one of ignore, extract")
	}
	if strings.TrimSpace(c.OpenAI.TranscriptionModel) == "" {
		return errors.New("openai.transcription_model is required")
	}
	return nil
}

func (c Config) MapPath(path string) string {
	for _, mapping := range c.Subtitles.PathMappings {
		if mapping.From == "" || mapping.To == "" {
			continue
		}
		if path == mapping.From {
			return mapping.To
		}
		prefix := strings.TrimRight(mapping.From, "/") + "/"
		if strings.HasPrefix(path, prefix) {
			return strings.TrimRight(mapping.To, "/") + "/" + strings.TrimPrefix(path, prefix)
		}
	}
	return path
}
