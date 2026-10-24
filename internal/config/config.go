package config

import (
	"errors"
	"math"
	"os"
	"strings"
	"time"

	sublang "github.com/Pedro-Revez-Silva/subtitler/internal/language"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DryRun     bool             `yaml:"dry_run"`
	StatePath  string           `yaml:"state_path"`
	TempDir    string           `yaml:"temp_dir"`
	Sonarr     ServiceConfig    `yaml:"sonarr"`
	Radarr     ServiceConfig    `yaml:"radarr"`
	OpenAI     OpenAIConfig     `yaml:"openai"`
	Telemetry  TelemetryConfig  `yaml:"telemetry"`
	Subtitles  SubtitleConfig   `yaml:"subtitles"`
	Processing ProcessingConfig `yaml:"processing"`
	Tools      ToolsConfig      `yaml:"tools"`
	Management ManagementConfig `yaml:"management"`
}

// Management selects the persistent, provider-first subtitle owner. Paid API
// clients are available only behind an explicit, budgeted fallback setting.
type ManagementConfig struct {
	Enabled                   bool        `yaml:"enabled" json:"enabled"`
	Python                    string      `yaml:"python" json:"python"`
	Script                    string      `yaml:"script" json:"script"`
	StateDir                  string      `yaml:"state_dir" json:"state_dir"`
	ProvidersFile             string      `yaml:"providers_file" json:"providers_file"`
	QuarantineDir             string      `yaml:"quarantine_dir" json:"quarantine_dir"`
	PruneEmbedded             bool        `yaml:"prune_embedded" json:"prune_embedded"`
	RetainMediaBackups        bool        `yaml:"retain_media_backups" json:"retain_media_backups"`
	LocalTranscription        bool        `yaml:"local_transcription" json:"local_transcription"`
	VerifyWords               bool        `yaml:"verify_words" json:"verify_words"`
	VerificationModel         string      `yaml:"verification_model" json:"verification_model"`
	VerificationFallbackModel string      `yaml:"verification_fallback_model" json:"verification_fallback_model"`
	MinFreeGB                 int         `yaml:"min_free_gb" json:"min_free_gb"`
	PaidFallback              bool        `yaml:"paid_fallback" json:"paid_fallback"`
	PaidDailyUSD              float64     `yaml:"paid_daily_usd" json:"paid_daily_usd"`
	MediaCacheGB              float64     `yaml:"media_cache_gb" json:"media_cache_gb"`
	Queues                    QueueConfig `yaml:"queues" json:"queues"`
}

// Queue limits are admission limits: running jobs finish safely under load.
type QueueConfig struct {
	BatchSize            int     `yaml:"batch_size" json:"batch_size"`
	FileWorkers          int     `yaml:"file_workers" json:"file_workers"`
	TranscriptionWorkers int     `yaml:"transcription_workers" json:"transcription_workers"`
	WorkerThreads        int     `yaml:"worker_threads" json:"worker_threads"`
	Adaptive             *bool   `yaml:"adaptive" json:"adaptive"`
	PollSeconds          int     `yaml:"poll_seconds" json:"poll_seconds"`
	MinMemoryGB          float64 `yaml:"min_memory_gb" json:"min_memory_gb"`
	MaxLoadPerCPU        float64 `yaml:"max_load_per_cpu" json:"max_load_per_cpu"`
	MaxIOPressure        float64 `yaml:"max_io_pressure" json:"max_io_pressure"`
	MaxDiskUtilization   float64 `yaml:"max_disk_utilization" json:"max_disk_utilization"`
	MaxDiskAwaitMS       float64 `yaml:"max_disk_await_ms" json:"max_disk_await_ms"`
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
	MaxChunkBytes      int64  `yaml:"max_chunk_bytes"`
	ChunkRetries       int    `yaml:"chunk_retries"`
}

type TelemetryConfig struct {
	Enabled     *bool  `yaml:"enabled"`
	SentryDSN   string `yaml:"sentry_dsn"`
	Environment string `yaml:"environment"`
	Release     string `yaml:"release"`
}

const DefaultTelemetrySentryDSN = "https://64f6ff280bac1f2acec4be2ac491ecbf@o4510866861719552.ingest.de.sentry.io/4511514028802128"

type SubtitleConfig struct {
	RequiredLanguages      []string       `yaml:"required_languages"`
	AudioLanguage          string         `yaml:"audio_language"`
	SourceAudioLanguages   []string       `yaml:"source_audio_languages"`
	SourceSubtitleLanguage string         `yaml:"source_subtitle_language"`
	Strategy               string         `yaml:"strategy"`
	Cleanup                CleanupConfig  `yaml:"cleanup"`
	Output                 OutputConfig   `yaml:"output"`
	Embedded               EmbeddedConfig `yaml:"embedded"`
	PathMappings           []PathMapping  `yaml:"path_mappings"`
	ProtectedSuffixes      []string       `yaml:"protected_suffixes"`
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
	MaxJobsPerScan   int           `yaml:"max_jobs_per_scan"`
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
	var languages []string
	seen := make(map[string]bool)
	for _, value := range cfg.Subtitles.RequiredLanguages {
		code, err := sublang.Normalize(value)
		if err != nil {
			return Config{}, errors.New("subtitles.required_languages must contain ISO language tags with optional script/country")
		}
		if !seen[code] {
			languages = append(languages, code)
			seen[code] = true
		}
	}
	cfg.Subtitles.RequiredLanguages = languages
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Default() Config {
	var cfg Config
	cfg.applyDefaults()
	return cfg
}

func (c *Config) applyDefaults() {
	q := &c.Management.Queues
	if q.BatchSize == 0 {
		q.BatchSize = 16
	}
	if q.FileWorkers == 0 {
		q.FileWorkers = 2
	}
	if q.TranscriptionWorkers == 0 {
		q.TranscriptionWorkers = 1
	}
	if q.WorkerThreads == 0 {
		q.WorkerThreads = 2
	}
	if q.Adaptive == nil {
		enabled := true
		q.Adaptive = &enabled
	}
	if q.PollSeconds == 0 {
		q.PollSeconds = 5
	}
	if q.MinMemoryGB == 0 {
		q.MinMemoryGB = 2
	}
	if q.MaxLoadPerCPU == 0 {
		q.MaxLoadPerCPU = 0.85
	}
	if q.MaxIOPressure == 0 {
		q.MaxIOPressure = 20
	}
	if q.MaxDiskUtilization == 0 {
		q.MaxDiskUtilization = 85
	}
	if q.MaxDiskAwaitMS == 0 {
		q.MaxDiskAwaitMS = 50
	}
	if c.Management.MediaCacheGB == 0 {
		c.Management.MediaCacheGB = 5
	}
	if c.Management.VerificationModel == "" {
		c.Management.VerificationModel = "base"
	}
	if c.Management.VerificationFallbackModel == "" {
		c.Management.VerificationFallbackModel = "small"
	}
	if c.StatePath == "" {
		c.StatePath = "subtitler-state.json"
	}
	if c.TempDir == "" {
		c.TempDir = os.TempDir()
	}
	if c.Sonarr.APIKey == "" {
		c.Sonarr.APIKey = os.Getenv("SONARR_API_KEY")
	}
	if c.Radarr.APIKey == "" {
		c.Radarr.APIKey = os.Getenv("RADARR_API_KEY")
	}
	if c.OpenAI.APIKey == "" {
		c.OpenAI.APIKey = os.Getenv("OPENAI_API_KEY")
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
	if c.OpenAI.MaxChunkBytes <= 0 {
		c.OpenAI.MaxChunkBytes = 24_000_000
	}
	if c.OpenAI.ChunkRetries < 0 {
		c.OpenAI.ChunkRetries = 0
	}
	if c.OpenAI.ChunkRetries == 0 {
		c.OpenAI.ChunkRetries = 2
	}
	c.applyTelemetryDefaults()
	if c.Telemetry.Environment == "" {
		c.Telemetry.Environment = "production"
	}
	if c.Subtitles.RequiredLanguages == nil {
		c.Subtitles.RequiredLanguages = []string{"en"}
	}
	if len(c.Subtitles.SourceAudioLanguages) == 0 {
		legacyAudioLanguage := strings.TrimSpace(c.Subtitles.AudioLanguage)
		if legacyAudioLanguage != "" && !strings.EqualFold(legacyAudioLanguage, "auto") {
			c.Subtitles.SourceAudioLanguages = []string{legacyAudioLanguage, "auto"}
		} else {
			c.Subtitles.SourceAudioLanguages = []string{"en", "auto"}
		}
	}
	if c.Subtitles.SourceSubtitleLanguage == "" {
		c.Subtitles.SourceSubtitleLanguage = "en"
	}
	if c.Subtitles.AudioLanguage == "" {
		c.Subtitles.AudioLanguage = c.Subtitles.SourceAudioLanguages[0]
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

func (c *Config) applyTelemetryDefaults() {
	if value, ok := parseBoolEnv(os.Getenv("SUBTITLER_TELEMETRY")); ok {
		c.Telemetry.Enabled = &value
	}
	if value, ok := parseBoolEnv(os.Getenv("SUBTITLER_TELEMETRY_ENABLED")); ok {
		c.Telemetry.Enabled = &value
	}
	if c.Telemetry.Enabled == nil {
		enabled := true
		c.Telemetry.Enabled = &enabled
	}
	if c.Telemetry.SentryDSN == "" {
		c.Telemetry.SentryDSN = os.Getenv("SENTRY_DSN")
	}
	if c.Telemetry.SentryDSN == "" {
		c.Telemetry.SentryDSN = DefaultTelemetrySentryDSN
	}
}

func (t TelemetryConfig) EnabledValue() bool {
	return t.Enabled == nil || *t.Enabled
}

func parseBoolEnv(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "enabled":
		return true, true
	case "0", "false", "no", "off", "disabled":
		return false, true
	default:
		return false, false
	}
}

func (c Config) validate() error {
	if len(c.Subtitles.RequiredLanguages) == 0 {
		return errors.New("subtitles.required_languages cannot be empty")
	}
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
	if strings.TrimSpace(c.Subtitles.SourceSubtitleLanguage) == "" {
		return errors.New("subtitles.source_subtitle_language is required")
	}
	for _, language := range c.Subtitles.SourceAudioLanguages {
		if strings.TrimSpace(language) == "" {
			return errors.New("subtitles.source_audio_languages cannot contain empty values")
		}
	}
	if c.Processing.MaxJobsPerScan < 0 {
		return errors.New("processing.max_jobs_per_scan cannot be negative")
	}
	if c.Management.Enabled && (c.Management.StateDir == "" || c.Management.QuarantineDir == "") {
		return errors.New("management.state_dir and management.quarantine_dir are required when enabled")
	}
	if c.Management.MinFreeGB < 0 {
		return errors.New("management.min_free_gb cannot be negative")
	}
	if v := c.Management.MediaCacheGB; math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
		return errors.New("management.media_cache_gb must be finite and between 0 and 100")
	}
	for _, model := range []string{c.Management.VerificationModel, c.Management.VerificationFallbackModel} {
		if model != "base" && model != "small" && model != "none" {
			return errors.New("management verification models must be base, small, or none")
		}
	}
	if c.Management.VerificationModel == "none" {
		return errors.New("management.verification_model cannot be none")
	}
	usd := c.Management.PaidDailyUSD
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 || (c.Management.PaidFallback && usd == 0) {
		return errors.New("management.paid_daily_usd must be finite and positive when paid fallback is enabled")
	}
	q := c.Management.Queues
	if q.BatchSize < 1 || q.BatchSize > 1000 || q.FileWorkers < 1 || q.FileWorkers > 16 || q.TranscriptionWorkers < 1 || q.TranscriptionWorkers > 16 || q.WorkerThreads < 1 || q.WorkerThreads > 16 || q.PollSeconds < 1 || q.PollSeconds > 60 {
		return errors.New("management queue workers/threads must be 1..16 and poll_seconds 1..60")
	}
	if math.IsNaN(q.MinMemoryGB) || math.IsInf(q.MinMemoryGB, 0) || q.MinMemoryGB < 0 || math.IsNaN(q.MaxLoadPerCPU) || q.MaxLoadPerCPU <= 0 || q.MaxLoadPerCPU > 1 || math.IsNaN(q.MaxIOPressure) || q.MaxIOPressure <= 0 || q.MaxIOPressure > 100 {
		return errors.New("invalid management queue memory, load, or IO pressure threshold")
	}
	if math.IsNaN(q.MaxDiskUtilization) || math.IsInf(q.MaxDiskUtilization, 0) || q.MaxDiskUtilization <= 0 || q.MaxDiskUtilization > 100 || math.IsNaN(q.MaxDiskAwaitMS) || math.IsInf(q.MaxDiskAwaitMS, 0) || q.MaxDiskAwaitMS <= 0 {
		return errors.New("invalid management queue disk utilization or latency threshold")
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
