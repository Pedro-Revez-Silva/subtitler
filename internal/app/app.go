package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Pedro-Revez-Silva/subtitler/internal/arr"
	"github.com/Pedro-Revez-Silva/subtitler/internal/config"
	"github.com/Pedro-Revez-Silva/subtitler/internal/media"
	"github.com/Pedro-Revez-Silva/subtitler/internal/openai"
	"github.com/Pedro-Revez-Silva/subtitler/internal/state"
	"github.com/Pedro-Revez-Silva/subtitler/internal/subtitle"
	"github.com/Pedro-Revez-Silva/subtitler/internal/telemetry"
)

type App struct {
	cfg       config.Config
	logger    *slog.Logger
	inspector mediaInspector
	openai    speechClient
	telemetry *telemetry.Client
	mode      string
}

type mediaInspector interface {
	CheckTools(context.Context) error
	DurationMS(context.Context, string) (int, error)
	AudioStreams(context.Context, string) ([]media.AudioStream, error)
	SubtitleStreams(context.Context, string) ([]media.SubtitleStream, error)
	ExtractAudioChunks(context.Context, string, media.AudioStream, string, int) ([]media.Chunk, func(), error)
	ExtractSubtitles(context.Context, string, map[string]media.SubtitleStreamOutput) error
}

type speechClient interface {
	TranscribeSRT(context.Context, string, string, string, string) (string, error)
	TranslateCues(context.Context, []subtitle.Cue, string, string, string) ([]subtitle.Cue, error)
}

func New(cfg config.Config, logger *slog.Logger, telemetryClient *telemetry.Client, mode string) *App {
	if telemetryClient == nil {
		telemetryClient = &telemetry.Client{}
	}
	if mode == "" {
		mode = "unknown"
	}
	return &App{
		cfg:       cfg,
		logger:    logger,
		inspector: media.Inspector{Logger: logger, FFmpegPath: cfg.Tools.FFmpeg, FFprobePath: cfg.Tools.FFprobe},
		openai:    openai.New(cfg.OpenAI.APIKey, cfg.OpenAI.BaseURL),
		telemetry: telemetryClient,
		mode:      mode,
	}
}

func (a *App) ScanAndProcess(ctx context.Context) error {
	if err := a.inspector.CheckTools(ctx); err != nil {
		return err
	}
	store, err := state.Open(a.cfg.StatePath)
	if err != nil {
		return err
	}

	items, err := a.mediaItems(ctx)
	if err != nil {
		return err
	}
	a.logger.Info("scan found media files", "count", len(items))

	processedJobs := 0
	for _, item := range items {
		if a.cfg.Processing.MaxJobsPerScan > 0 && processedJobs >= a.cfg.Processing.MaxJobsPerScan {
			a.logger.Info("scan job limit reached", "max_jobs_per_scan", a.cfg.Processing.MaxJobsPerScan)
			break
		}
		item.Path = a.cfg.MapPath(item.Path)
		didWork, err := a.processItem(ctx, store, item)
		if didWork {
			processedJobs++
		}
		if err != nil {
			a.logger.Error("failed to process media item", "path", item.Path, "error", err)
			a.recordFailure(store, item, err)
		}
		if err := store.Save(); err != nil {
			return err
		}
	}
	a.logger.Info("scan finished", "processed_jobs", processedJobs, "max_jobs_per_scan", a.cfg.Processing.MaxJobsPerScan)
	return store.Save()
}

func (a *App) GenerateOne(ctx context.Context, videoPath string) error {
	if err := a.inspector.CheckTools(ctx); err != nil {
		return err
	}
	store, err := state.Open(a.cfg.StatePath)
	if err != nil {
		return err
	}
	item := arr.MediaItem{
		Source:  "manual",
		Path:    videoPath,
		Title:   strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath)),
		Context: strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath)),
	}
	if _, err := a.processItem(ctx, store, item); err != nil {
		a.recordFailure(store, item, err)
		_ = store.Save()
		return err
	}
	return store.Save()
}

func (a *App) Inspect(ctx context.Context, videoPath string) error {
	if err := a.inspector.CheckTools(ctx); err != nil {
		return err
	}
	streams, err := a.inspector.AudioStreams(ctx, videoPath)
	if err != nil {
		return err
	}
	for _, stream := range streams {
		a.logger.Info("audio stream", "index", stream.Index, "language", stream.Language, "title", stream.Title, "default", stream.Default, "codec", stream.Codec)
	}
	sidecars, err := subtitle.FindSidecars(videoPath)
	if err != nil {
		return err
	}
	for _, sidecar := range sidecars {
		a.logger.Info("sidecar subtitle", "path", sidecar.Path, "language", sidecar.Language, "ext", sidecar.Ext)
	}
	return nil
}

func (a *App) Doctor(ctx context.Context) error {
	if err := a.inspector.CheckTools(ctx); err != nil {
		return err
	}
	a.logger.Info("media tools available", "ffmpeg", a.cfg.Tools.FFmpeg, "ffprobe", a.cfg.Tools.FFprobe)
	if a.cfg.OpenAI.APIKey == "" {
		a.logger.Warn("openai.api_key is empty; dry runs can work, generation cannot")
	} else {
		a.logger.Info("openai api key configured")
	}
	if a.cfg.Radarr.URL == "" && a.cfg.Sonarr.URL == "" {
		a.logger.Warn("no Sonarr/Radarr URL configured; daemon scan will not have a media source")
	}
	if err := a.checkARR(ctx, "sonarr", a.cfg.Sonarr); err != nil {
		return err
	}
	if err := a.checkARR(ctx, "radarr", a.cfg.Radarr); err != nil {
		return err
	}
	return nil
}

func (a *App) checkARR(ctx context.Context, name string, service config.ServiceConfig) error {
	if service.URL == "" {
		return nil
	}
	if service.APIKey == "" {
		a.logger.Warn("ARR API key is empty; daemon scan cannot use this service", "service", name, "url", service.URL)
		return nil
	}
	if err := arr.New(service.URL, service.APIKey).SystemStatus(ctx); err != nil {
		return fmt.Errorf("%s connectivity check failed: %w", name, err)
	}
	a.logger.Info("ARR service reachable", "service", name, "url", service.URL)
	return nil
}

func (a *App) mediaItems(ctx context.Context) ([]arr.MediaItem, error) {
	var items []arr.MediaItem
	if a.cfg.Radarr.URL == "" && a.cfg.Sonarr.URL == "" {
		return nil, fmt.Errorf("at least one of radarr.url or sonarr.url is required for scanning")
	}
	if a.cfg.Radarr.URL != "" {
		radarrItems, err := arr.New(a.cfg.Radarr.URL, a.cfg.Radarr.APIKey).RadarrMovies(ctx)
		if err != nil {
			return nil, fmt.Errorf("radarr: %w", err)
		}
		items = append(items, radarrItems...)
	}
	if a.cfg.Sonarr.URL != "" {
		sonarrItems, err := arr.New(a.cfg.Sonarr.URL, a.cfg.Sonarr.APIKey).SonarrEpisodes(ctx)
		if err != nil {
			return nil, fmt.Errorf("sonarr: %w", err)
		}
		items = append(items, sonarrItems...)
	}
	return items, nil
}

func (a *App) processItem(ctx context.Context, store *state.Store, item arr.MediaItem) (bool, error) {
	info, err := os.Stat(item.Path)
	if err != nil {
		return false, err
	}

	fileState, _ := store.Get(item.Path)
	mediaUnchanged := fileState.FileSize == info.Size() && fileState.ModTimeUnix == info.ModTime().Unix()
	if fileState.Languages == nil {
		fileState.Languages = map[string]state.LangState{}
	}
	fileState.Source = item.Source
	fileState.FileSize = info.Size()
	fileState.ModTimeUnix = info.ModTime().Unix()

	sidecars, err := subtitle.FindSidecars(item.Path)
	if err != nil {
		return false, err
	}

	if a.shouldDelayRetry(fileState, mediaUnchanged) {
		a.logger.Info("skipping failed media until retry delay passes", "path", item.Path, "attempts", fileState.Attempts)
		store.Put(item.Path, fileState)
		return false, nil
	}

	generatedPaths := generatedSubtitlePaths(fileState)
	if err := a.cleanupSidecars(item.Path, sidecars, generatedPaths); err != nil {
		return false, err
	}

	toGenerate := a.languagesToGenerate(item.Path, fileState, mediaUnchanged)
	didWork := len(toGenerate) > 0
	if len(toGenerate) > 0 && a.cfg.Subtitles.Embedded.Action == "extract" {
		extracted, err := a.extractEmbeddedSubtitles(ctx, item.Path, toGenerate, fileState)
		if err != nil {
			return didWork, err
		}
		if len(extracted) > 0 {
			for language, outputPath := range extracted {
				fileState.Languages[language] = state.LangState{
					OutputPath: outputPath,
					Generated:  true,
					UpdatedAt:  time.Now(),
				}
			}
			store.Put(item.Path, fileState)
			toGenerate = subtractLanguages(toGenerate, extracted)
		}
	}
	if len(toGenerate) == 0 {
		a.logger.Info("subtitles already present", "path", item.Path)
		fileState.LastError = ""
		fileState.Attempts = 0
		store.Put(item.Path, fileState)
		return didWork, nil
	}
	if a.cfg.DryRun {
		a.logger.Info("dry run would generate subtitles", "path", item.Path, "languages", toGenerate)
		store.Put(item.Path, fileState)
		return true, nil
	}
	if a.cfg.OpenAI.APIKey == "" {
		return true, fmt.Errorf("openai.api_key is required to generate subtitles")
	}

	sourceSRT, sourceLanguage, err := a.transcribe(ctx, item)
	if err != nil {
		return true, err
	}
	sourceCues, err := subtitle.ParseSRT(sourceSRT)
	if err != nil {
		return true, err
	}
	if durationMS, err := a.inspector.DurationMS(ctx, item.Path); err != nil {
		a.logger.Warn("could not determine media duration for subtitle trimming", "path", item.Path, "error", err)
	} else {
		before := len(sourceCues)
		sourceCues = subtitle.TrimToDuration(sourceCues, durationMS)
		if len(sourceCues) != before {
			a.logger.Info("trimmed subtitles to media duration", "path", item.Path, "before", before, "after", len(sourceCues))
		}
		sourceSRT = subtitle.FormatSRT(sourceCues)
	}
	if err := subtitle.ValidateGenerated(sourceCues); err != nil {
		return true, err
	}

	for _, language := range toGenerate {
		outputCues := sourceCues
		outputSRT := sourceSRT
		if !sameLanguage(language, sourceLanguage) {
			a.logger.Info("translating subtitles", "path", item.Path, "from", sourceLanguage, "to", language)
			translatedCues, err := a.openai.TranslateCues(ctx, sourceCues, a.cfg.OpenAI.TranslationModel, language, a.contextFor(item))
			if err != nil {
				return true, err
			}
			outputCues = translatedCues
			outputSRT = subtitle.FormatSRT(translatedCues)
		}
		if err := subtitle.ValidateGenerated(outputCues); err != nil {
			return true, err
		}
		outputPath := subtitle.OutputPath(item.Path, language, a.cfg.Subtitles.Output.Title)
		if err := os.WriteFile(outputPath, []byte(outputSRT), 0o644); err != nil {
			return true, err
		}
		fileState.Languages[language] = state.LangState{
			OutputPath: outputPath,
			Generated:  true,
			UpdatedAt:  time.Now(),
		}
		a.logger.Info("wrote subtitles", "path", outputPath, "language", language)
	}

	fileState.LastError = ""
	fileState.Attempts = 0
	store.Put(item.Path, fileState)
	return true, nil
}

func (a *App) transcribe(ctx context.Context, item arr.MediaItem) (string, string, error) {
	streams, err := a.inspector.AudioStreams(ctx, item.Path)
	if err != nil {
		return "", "", err
	}
	stream, err := media.SelectAudioStreamByPreference(streams, a.cfg.Subtitles.SourceAudioLanguages)
	if err != nil {
		return "", "", err
	}
	transcriptionLanguage, sourceLanguage := transcriptionLanguages(a.cfg.Subtitles.SourceSubtitleLanguage, stream.Language)
	a.logger.Info("selected audio stream", "path", item.Path, "index", stream.Index, "language", stream.Language, "title", stream.Title)

	chunks, cleanup, err := a.inspector.ExtractAudioChunks(ctx, item.Path, stream, a.cfg.TempDir, a.cfg.OpenAI.MaxChunkSeconds)
	if err != nil {
		return "", "", err
	}
	defer cleanup()
	if len(chunks) == 0 {
		return "", "", fmt.Errorf("no audio chunks were created")
	}

	var cues []subtitle.Cue
	for _, chunk := range chunks {
		a.logger.Info("transcribing audio chunk", "path", item.Path, "chunk", chunk.ChunkNumber, "offset_ms", chunk.OffsetMS)
		srt, err := a.openai.TranscribeSRT(ctx, chunk.Path, a.cfg.OpenAI.TranscriptionModel, a.contextFor(item), openAILanguage(transcriptionLanguage))
		if err != nil {
			return "", "", err
		}
		chunkCues, err := subtitle.ParseSRT(srt)
		if err != nil {
			return "", "", err
		}
		cues = append(cues, subtitle.Offset(chunkCues, chunk.OffsetMS)...)
	}
	return subtitle.FormatSRT(cues), sourceLanguage, nil
}

func (a *App) extractEmbeddedSubtitles(ctx context.Context, videoPath string, languages []string, fileState state.FileState) (map[string]string, error) {
	streams, err := a.inspector.SubtitleStreams(ctx, videoPath)
	if err != nil {
		return nil, err
	}
	extracted := map[string]string{}
	outputs := map[string]media.SubtitleStreamOutput{}
	for _, language := range languages {
		if langState, ok := fileState.Languages[language]; ok && langState.Generated && fileExists(langState.OutputPath) {
			continue
		}
		stream, ok := findSubtitleStream(streams, language)
		if !ok {
			continue
		}
		outputPath := subtitle.OutputPath(videoPath, language, a.cfg.Subtitles.Embedded.Title)
		if a.cfg.DryRun {
			a.logger.Info("dry run would extract embedded subtitle", "path", videoPath, "language", language, "stream", stream.Index, "output", outputPath)
			extracted[language] = outputPath
			continue
		}
		a.logger.Info("extracting embedded subtitle", "path", videoPath, "language", language, "stream", stream.Index, "output", outputPath)
		outputs[language] = media.SubtitleStreamOutput{Stream: stream, Path: outputPath}
		extracted[language] = outputPath
	}
	if len(outputs) > 0 {
		if err := a.inspector.ExtractSubtitles(ctx, videoPath, outputs); err != nil {
			return nil, err
		}
	}
	return extracted, nil
}

func (a *App) cleanupSidecars(videoPath string, sidecars []subtitle.Sidecar, generatedPaths map[string]bool) error {
	if a.cfg.Subtitles.Cleanup.ExternalSubtitles == "keep" {
		return nil
	}
	for _, sidecar := range sidecars {
		if generatedPaths[sidecar.Path] {
			continue
		}
		if subtitle.IsProtected(sidecar, a.cfg.Subtitles.ProtectedSuffixes) {
			continue
		}
		shouldClean := false
		if a.cfg.Subtitles.Strategy == "generated_only" || a.cfg.Subtitles.Strategy == "force" {
			shouldClean = true
		}
		if sidecar.Language != "" && !containsLanguage(a.cfg.Subtitles.RequiredLanguages, sidecar.Language) {
			shouldClean = true
		}
		if !shouldClean {
			continue
		}
		if a.cfg.DryRun {
			a.logger.Info("dry run would clean sidecar subtitle", "path", sidecar.Path)
			continue
		}
		switch a.cfg.Subtitles.Cleanup.ExternalSubtitles {
		case "delete":
			a.logger.Info("deleting sidecar subtitle", "path", sidecar.Path)
			if err := os.Remove(sidecar.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
		case "quarantine":
			backupDir := filepath.Join(filepath.Dir(videoPath), ".subtitler-backup")
			if err := os.MkdirAll(backupDir, 0o755); err != nil {
				return err
			}
			target := filepath.Join(backupDir, filepath.Base(sidecar.Path))
			a.logger.Info("quarantining sidecar subtitle", "from", sidecar.Path, "to", target)
			if err := os.Rename(sidecar.Path, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) languagesToGenerate(videoPath string, fileState state.FileState, mediaUnchanged bool) []string {
	if a.cfg.Subtitles.Strategy == "force" {
		return slices.Clone(a.cfg.Subtitles.RequiredLanguages)
	}
	if a.cfg.Subtitles.Strategy == "generated_only" {
		var needed []string
		for _, language := range a.cfg.Subtitles.RequiredLanguages {
			langState, ok := fileState.Languages[language]
			if ok && mediaUnchanged && langState.Generated && fileExists(langState.OutputPath) {
				continue
			}
			needed = append(needed, language)
		}
		return needed
	}
	sidecars, err := subtitle.FindSidecars(videoPath)
	if err != nil {
		return slices.Clone(a.cfg.Subtitles.RequiredLanguages)
	}
	present := []string{}
	for _, sidecar := range sidecars {
		if sidecar.Language != "" {
			present = append(present, sidecar.Language)
		}
	}
	var missing []string
	for _, language := range a.cfg.Subtitles.RequiredLanguages {
		if !containsLanguage(present, language) {
			missing = append(missing, language)
		}
	}
	return missing
}

func (a *App) contextFor(item arr.MediaItem) string {
	parts := []string{}
	if item.Context != "" {
		parts = append(parts, item.Context)
	}
	if a.cfg.OpenAI.Context != "" {
		parts = append(parts, a.cfg.OpenAI.Context)
	}
	return strings.Join(parts, "\n")
}

func (a *App) recordFailure(store *state.Store, item arr.MediaItem, err error) {
	fileState, _ := store.Get(item.Path)
	fileState.Source = item.Source
	fileState.LastError = err.Error()
	fileState.Attempts++
	store.Put(item.Path, fileState)
}

func sameLanguage(a, b string) bool {
	a = languageBase(a)
	b = languageBase(b)
	return a != "" && a == b
}

func containsLanguage(languages []string, candidate string) bool {
	for _, language := range languages {
		if sameLanguage(language, candidate) {
			return true
		}
	}
	return false
}

func languageBase(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	switch value {
	case "", "auto", "und":
		return ""
	case "eng", "english":
		return "en"
	case "por", "portuguese":
		return "pt"
	case "chi", "zho", "cmn", "chinese":
		return "zh"
	default:
		return strings.Split(value, "-")[0]
	}
}

func transcriptionLanguages(requestedLanguage, streamLanguage string) (string, string) {
	requestLanguage := strings.TrimSpace(requestedLanguage)
	if requestLanguage == "" {
		requestLanguage = "auto"
	}
	sourceLanguage := requestLanguage
	if languageBase(streamLanguage) != "" {
		sourceLanguage = streamLanguage
		if languageBase(requestLanguage) != "" {
			requestLanguage = streamLanguage
		}
	}
	return requestLanguage, sourceLanguage
}

func openAILanguage(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "" || language == "auto" {
		return language
	}
	switch language {
	case "pt-pt", "pt-br":
		return "pt"
	default:
		return strings.Split(language, "-")[0]
	}
}

func findSubtitleStream(streams []media.SubtitleStream, language string) (media.SubtitleStream, bool) {
	for _, stream := range streams {
		if sameLanguage(stream.Language, language) {
			return stream, true
		}
	}
	return media.SubtitleStream{}, false
}

func subtractLanguages(languages []string, extracted map[string]string) []string {
	var remaining []string
	for _, language := range languages {
		if _, ok := extracted[language]; !ok {
			remaining = append(remaining, language)
		}
	}
	return remaining
}

func generatedSubtitlePaths(fileState state.FileState) map[string]bool {
	paths := map[string]bool{}
	for _, langState := range fileState.Languages {
		if langState.Generated && langState.OutputPath != "" {
			paths[langState.OutputPath] = true
		}
	}
	return paths
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func (a *App) shouldDelayRetry(fileState state.FileState, mediaUnchanged bool) bool {
	if !mediaUnchanged {
		return false
	}
	if fileState.LastError == "" || fileState.Attempts < a.cfg.Processing.MaxAttempts {
		return false
	}
	return time.Since(fileState.UpdatedAt) < a.cfg.Processing.RetryFailedAfter
}
