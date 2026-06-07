package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Pedro-Revez-Silva/subtitler/internal/arr"
	"github.com/Pedro-Revez-Silva/subtitler/internal/config"
	"github.com/Pedro-Revez-Silva/subtitler/internal/media"
	"github.com/Pedro-Revez-Silva/subtitler/internal/state"
	"github.com/Pedro-Revez-Silva/subtitler/internal/subtitle"
)

func TestMissingOnlyTreatsJellyfinPortugueseSidecarAsPresent(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "Episode.mkv")
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Episode.subtitler.pt.srt"), []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New(config.Config{
		Subtitles: config.SubtitleConfig{
			RequiredLanguages: []string{"pt-PT"},
			Strategy:          "missing_only",
		},
	}, slog.Default(), nil, "test")

	missing := app.languagesToGenerate(videoPath, state.FileState{}, true, nil)
	if len(missing) != 0 {
		t.Fatalf("expected pt sidecar to satisfy pt-PT, got missing languages %#v", missing)
	}
}

func TestMissingOnlyTreatsEmbeddedSubtitleAsPresentWhenIgnoringEmbedded(t *testing.T) {
	videoPath := writeVideo(t)
	app := New(config.Config{
		Subtitles: config.SubtitleConfig{
			RequiredLanguages: []string{"en", "pt-PT"},
			Strategy:          "missing_only",
			Embedded:          config.EmbeddedConfig{Action: "ignore"},
		},
	}, slog.Default(), nil, "test")

	missing := app.languagesToGenerate(videoPath, state.FileState{}, true, []media.SubtitleStream{
		{Index: 2, Language: "en", Title: "English"},
		{Index: 3, Language: "pt", Title: "Portuguese"},
	})
	if len(missing) != 0 {
		t.Fatalf("expected embedded subtitles to satisfy required languages, got %#v", missing)
	}
}

func TestMissingOnlyDoesNotTreatForcedEmbeddedSubtitleAsPresent(t *testing.T) {
	videoPath := writeVideo(t)
	app := New(config.Config{
		Subtitles: config.SubtitleConfig{
			RequiredLanguages: []string{"en"},
			Strategy:          "missing_only",
			Embedded:          config.EmbeddedConfig{Action: "ignore"},
			ProtectedSuffixes: []string{"forced"},
		},
	}, slog.Default(), nil, "test")

	missing := app.languagesToGenerate(videoPath, state.FileState{}, true, []media.SubtitleStream{
		{Index: 2, Language: "en", Title: "English Forced", Forced: true},
	})
	if len(missing) != 1 || missing[0] != "en" {
		t.Fatalf("expected forced-only embedded subtitle to be missing, got %#v", missing)
	}
}

func TestCleanupKeepsPortugueseSidecarWhenPortugueseRequired(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "Episode.mkv")
	sidecarPath := filepath.Join(dir, "Episode.pt.srt")
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecarPath, []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New(config.Config{
		Subtitles: config.SubtitleConfig{
			RequiredLanguages: []string{"pt-PT"},
			Strategy:          "missing_only",
			Cleanup:           config.CleanupConfig{ExternalSubtitles: "delete"},
		},
	}, slog.Default(), nil, "test")

	sidecars := []subtitle.Sidecar{{Path: sidecarPath, Language: "pt", Ext: ".srt", Suffixes: []string{"pt"}}}
	if err := app.cleanupSidecars(videoPath, sidecars, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sidecarPath); err != nil {
		t.Fatalf("expected Portuguese sidecar to be kept: %v", err)
	}
}

func TestTranscriptionLanguagesUsesKnownStreamAsSourceWithAutoRequest(t *testing.T) {
	requestLanguage, sourceLanguage := transcriptionLanguages("auto", "en")
	if requestLanguage != "auto" {
		t.Fatalf("expected OpenAI request to stay auto, got %q", requestLanguage)
	}
	if sourceLanguage != "en" {
		t.Fatalf("expected known stream language to become source language, got %q", sourceLanguage)
	}
}

func TestTranscriptionLanguagesConstrainsExplicitRequestToSelectedStream(t *testing.T) {
	requestLanguage, sourceLanguage := transcriptionLanguages("pt-PT", "pt")
	if requestLanguage != "pt" {
		t.Fatalf("expected explicit request to follow selected stream tag, got %q", requestLanguage)
	}
	if sourceLanguage != "pt" {
		t.Fatalf("expected selected stream tag as source language, got %q", sourceLanguage)
	}
}

func TestProcessItemGeneratesSourceLanguageSubtitle(t *testing.T) {
	videoPath := writeVideo(t)
	store := newStore(t)
	app := testApp(config.Config{
		DryRun:    false,
		TempDir:   t.TempDir(),
		OpenAI:    config.OpenAIConfig{APIKey: "test", TranscriptionModel: "whisper-1", MaxChunkSeconds: 1200},
		Subtitles: config.SubtitleConfig{RequiredLanguages: []string{"en"}, AudioLanguage: "auto", Strategy: "missing_only", Output: config.OutputConfig{Title: "subtitler"}},
	})

	didWork, err := app.processItem(context.Background(), store, itemFor(videoPath))
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("expected generation work")
	}
	outputPath := filepath.Join(filepath.Dir(videoPath), "Episode.subtitler.en.srt")
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Hello.") {
		t.Fatalf("expected source SRT to be written, got:\n%s", string(data))
	}
	if app.openai.(*fakeSpeech).translations != 0 {
		t.Fatal("expected matching source language to avoid translation")
	}
}

func TestProcessItemTranslatesTargetLanguage(t *testing.T) {
	videoPath := writeVideo(t)
	store := newStore(t)
	app := testApp(config.Config{
		DryRun:    false,
		TempDir:   t.TempDir(),
		OpenAI:    config.OpenAIConfig{APIKey: "test", TranscriptionModel: "whisper-1", TranslationModel: "gpt-test", MaxChunkSeconds: 1200},
		Subtitles: config.SubtitleConfig{RequiredLanguages: []string{"pt-PT"}, AudioLanguage: "auto", Strategy: "missing_only", Output: config.OutputConfig{Title: "subtitler"}},
	})

	didWork, err := app.processItem(context.Background(), store, itemFor(videoPath))
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("expected generation work")
	}
	outputPath := filepath.Join(filepath.Dir(videoPath), "Episode.subtitler.pt.srt")
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Ola.") {
		t.Fatalf("expected translated SRT, got:\n%s", string(data))
	}
	if app.openai.(*fakeSpeech).translations != 1 {
		t.Fatal("expected one translation call")
	}
}

func TestProcessItemPrefersConfiguredSourceAudioLanguage(t *testing.T) {
	videoPath := writeVideo(t)
	store := newStore(t)
	app := testApp(config.Config{
		DryRun:  false,
		TempDir: t.TempDir(),
		OpenAI:  config.OpenAIConfig{APIKey: "test", TranscriptionModel: "whisper-1", MaxChunkSeconds: 1200},
		Subtitles: config.SubtitleConfig{
			RequiredLanguages:      []string{"en"},
			SourceAudioLanguages:   []string{"en", "auto"},
			SourceSubtitleLanguage: "en",
			Strategy:               "missing_only",
			Output:                 config.OutputConfig{Title: "subtitler"},
		},
	})
	app.inspector = fakeInspector{
		audio: []media.AudioStream{
			{Index: 0, Language: "pt", Default: true},
			{Index: 1, Language: "en", Default: false},
		},
	}

	if _, err := app.processItem(context.Background(), store, itemFor(videoPath)); err != nil {
		t.Fatal(err)
	}
	speech := app.openai.(*fakeSpeech)
	if len(speech.transcriptionLanguages) != 1 || speech.transcriptionLanguages[0] != "en" {
		t.Fatalf("expected English transcription request, got %#v", speech.transcriptionLanguages)
	}
	if speech.translations != 0 {
		t.Fatal("expected English source output to avoid translation")
	}
}

func TestProcessItemStopsTranscribingAfterEarlyQualityFailure(t *testing.T) {
	videoPath := writeVideo(t)
	store := newStore(t)
	app := testApp(config.Config{
		DryRun:    false,
		TempDir:   t.TempDir(),
		OpenAI:    config.OpenAIConfig{APIKey: "test", TranscriptionModel: "whisper-1", MaxChunkSeconds: 1200},
		Subtitles: config.SubtitleConfig{RequiredLanguages: []string{"en"}, Strategy: "missing_only", Output: config.OutputConfig{Title: "subtitler"}},
	})
	app.inspector = fakeInspector{chunks: []media.Chunk{
		{Path: "chunk-1.mp3", OffsetMS: 0, ChunkNumber: 1},
		{Path: "chunk-2.mp3", OffsetMS: 60_000, ChunkNumber: 2},
	}}
	app.openai = &fakeSpeech{transcriptionOutputs: []string{
		"1\n00:00:01,000 --> 00:00:02,000\nThe.Movie.2026.2160p.WEB-DL.HEVC\n\n",
		"1\n00:00:01,000 --> 00:00:02,000\nHello.\n\n",
	}}

	didWork, err := app.processItem(context.Background(), store, itemFor(videoPath))
	if err == nil {
		t.Fatal("expected quality failure")
	}
	if !didWork {
		t.Fatal("expected attempted generation work")
	}
	if !strings.Contains(err.Error(), "chunk 1 failed quality check") {
		t.Fatalf("expected chunk quality failure, got %v", err)
	}
	if app.openai.(*fakeSpeech).transcriptions != 1 {
		t.Fatalf("expected transcription to stop after first bad chunk, got %d calls", app.openai.(*fakeSpeech).transcriptions)
	}
}

func TestProcessItemRetriesBadChunkAndKeepsGoodRetry(t *testing.T) {
	videoPath := writeVideo(t)
	store := newStore(t)
	app := testApp(config.Config{
		DryRun:  false,
		TempDir: t.TempDir(),
		OpenAI: config.OpenAIConfig{
			APIKey:             "test",
			TranscriptionModel: "whisper-1",
			MaxChunkSeconds:    1200,
			ChunkRetries:       1,
		},
		Subtitles: config.SubtitleConfig{RequiredLanguages: []string{"en"}, Strategy: "missing_only", Output: config.OutputConfig{Title: "subtitler"}},
	})
	app.inspector = fakeInspector{chunks: []media.Chunk{{Path: "chunk-1.mp3", OffsetMS: 0, ChunkNumber: 1}}}
	app.openai = &fakeSpeech{transcriptionOutputs: []string{
		"1\n00:00:01,000 --> 00:00:02,000\nThe.Movie.2026.2160p.WEB-DL.HEVC\n\n",
		"1\n00:00:01,000 --> 00:00:02,000\nHello.\n\n",
	}}

	didWork, err := app.processItem(context.Background(), store, itemFor(videoPath))
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("expected generation work")
	}
	if app.openai.(*fakeSpeech).transcriptions != 2 {
		t.Fatalf("expected one chunk retry, got %d transcription calls", app.openai.(*fakeSpeech).transcriptions)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(videoPath), "Episode.subtitler.en.srt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Hello.") {
		t.Fatalf("expected clean retry output, got:\n%s", string(data))
	}
}

func TestProcessItemDryRunDoesNotWriteSubtitle(t *testing.T) {
	videoPath := writeVideo(t)
	store := newStore(t)
	app := testApp(config.Config{
		DryRun:    true,
		Subtitles: config.SubtitleConfig{RequiredLanguages: []string{"en"}, Strategy: "missing_only", Output: config.OutputConfig{Title: "subtitler"}},
	})

	didWork, err := app.processItem(context.Background(), store, itemFor(videoPath))
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("expected dry run to identify work")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(videoPath), "Episode.subtitler.en.srt")); !os.IsNotExist(err) {
		t.Fatalf("expected no generated file in dry run, got %v", err)
	}
}

func TestProcessItemExtractsEmbeddedBeforeTranscribing(t *testing.T) {
	videoPath := writeVideo(t)
	store := newStore(t)
	app := testApp(config.Config{
		DryRun:    false,
		Subtitles: config.SubtitleConfig{RequiredLanguages: []string{"pt-PT"}, Strategy: "missing_only", Embedded: config.EmbeddedConfig{Action: "extract", Title: "official"}},
	})
	app.inspector = fakeInspector{subtitles: []media.SubtitleStream{{Index: 1, Language: "pt", Codec: "subrip"}}}

	didWork, err := app.processItem(context.Background(), store, itemFor(videoPath))
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("expected extraction work")
	}
	outputPath := filepath.Join(filepath.Dir(videoPath), "Episode.official.pt.srt")
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Embedded") {
		t.Fatalf("expected embedded subtitle output, got:\n%s", string(data))
	}
	if app.openai.(*fakeSpeech).transcriptions != 0 {
		t.Fatal("expected embedded extraction to avoid transcription")
	}
}

func TestProcessItemSkipsFailedMediaUntilRetryDelay(t *testing.T) {
	videoPath := writeVideo(t)
	store := newStore(t)
	app := testApp(config.Config{
		Processing: config.ProcessingConfig{MaxAttempts: 3, RetryFailedAfter: time.Hour},
		Subtitles:  config.SubtitleConfig{RequiredLanguages: []string{"en"}, Strategy: "missing_only"},
	})
	info, err := os.Stat(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	store.Put(videoPath, state.FileState{
		FileSize:    info.Size(),
		ModTimeUnix: info.ModTime().Unix(),
		LastError:   "previous failure",
		Attempts:    3,
		UpdatedAt:   time.Now(),
	})

	didWork, err := app.processItem(context.Background(), store, itemFor(videoPath))
	if err != nil {
		t.Fatal(err)
	}
	if didWork {
		t.Fatal("expected retry-delayed media to be skipped")
	}
}

func TestCleanupDeletesUnwantedSidecar(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "Episode.mkv")
	sidecarPath := filepath.Join(dir, "Episode.es.srt")
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecarPath, []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{
		Subtitles: config.SubtitleConfig{
			RequiredLanguages: []string{"en"},
			Strategy:          "missing_only",
			Cleanup:           config.CleanupConfig{ExternalSubtitles: "delete"},
		},
	}, slog.Default(), nil, "test")

	sidecars := []subtitle.Sidecar{{Path: sidecarPath, Language: "es", Ext: ".srt", Suffixes: []string{"es"}}}
	if err := app.cleanupSidecars(videoPath, sidecars, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sidecarPath); !os.IsNotExist(err) {
		t.Fatalf("expected sidecar to be deleted, got %v", err)
	}
}

func TestCheckARRReportsConnectivityError(t *testing.T) {
	app := testApp(config.Config{})
	err := app.checkARR(context.Background(), "sonarr", config.ServiceConfig{URL: "http://127.0.0.1:1", APIKey: "test"})
	if err == nil {
		t.Fatal("expected connectivity error")
	}
}

func TestRecordFailureIncrementsAttempts(t *testing.T) {
	store := newStore(t)
	videoPath := writeVideo(t)
	app := testApp(config.Config{})
	app.recordFailure(store, itemFor(videoPath), errors.New("boom"))
	app.recordFailure(store, itemFor(videoPath), errors.New("boom again"))

	fileState, ok := store.Get(videoPath)
	if !ok {
		t.Fatal("expected recorded state")
	}
	if fileState.Attempts != 2 || fileState.LastError != "boom again" {
		t.Fatalf("unexpected failure state: %#v", fileState)
	}
	info, err := os.Stat(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	if fileState.FileSize != info.Size() || fileState.ModTimeUnix != info.ModTime().Unix() {
		t.Fatalf("expected failure state to track media fingerprint: %#v", fileState)
	}
}

func TestGenerateOneProcessesManualFile(t *testing.T) {
	videoPath := writeVideo(t)
	cfg := config.Config{
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		TempDir:   t.TempDir(),
		OpenAI:    config.OpenAIConfig{APIKey: "test", TranscriptionModel: "whisper-1", MaxChunkSeconds: 1200},
		Subtitles: config.SubtitleConfig{RequiredLanguages: []string{"en"}, AudioLanguage: "auto", Strategy: "missing_only", Output: config.OutputConfig{Title: "subtitler"}},
	}
	app := testApp(cfg)

	if err := app.GenerateOne(context.Background(), videoPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(videoPath), "Episode.subtitler.en.srt")); err != nil {
		t.Fatal(err)
	}
}

func TestInspectListsStreamsAndSidecars(t *testing.T) {
	videoPath := writeVideo(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(videoPath), "Episode.en.srt"), []byte("sub"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := testApp(config.Config{})
	if err := app.Inspect(context.Background(), videoPath); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorChecksToolsAndARRStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/system/status" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer server.Close()

	app := testApp(config.Config{
		Sonarr: config.ServiceConfig{URL: server.URL, APIKey: "sonarr-key"},
		Radarr: config.ServiceConfig{URL: server.URL, APIKey: "radarr-key"},
		OpenAI: config.OpenAIConfig{APIKey: "test"},
	})
	if err := app.Doctor(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestScanAndProcessUsesARRSourcesAndJobLimit(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "Movie.mkv")
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":    1,
				"title": "Movie",
				"year":  2026,
				"path":  dir,
				"movieFile": map[string]any{
					"id":   10,
					"path": videoPath,
				},
			}})
		case "/api/v3/series":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	app := testApp(config.Config{
		DryRun:    true,
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		Radarr:    config.ServiceConfig{URL: server.URL, APIKey: "radarr-key"},
		Sonarr:    config.ServiceConfig{URL: server.URL, APIKey: "sonarr-key"},
		Subtitles: config.SubtitleConfig{RequiredLanguages: []string{"en"}, Strategy: "missing_only"},
		Processing: config.ProcessingConfig{
			MaxJobsPerScan:   1,
			MaxAttempts:      3,
			RetryFailedAfter: time.Hour,
		},
	})
	if err := app.ScanAndProcess(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAppLanguageHelpers(t *testing.T) {
	if !sameLanguage("pt-PT", "por") || sameLanguage("auto", "en") {
		t.Fatal("unexpected language equivalence")
	}
	for input, want := range map[string]string{"english": "en", "chinese": "zh", "": "", "und": ""} {
		if got := languageBase(input); got != want {
			t.Fatalf("languageBase(%q)=%q want %q", input, got, want)
		}
	}
	for input, want := range map[string]string{"pt-BR": "pt", "en-US": "en", "auto": "auto", "": ""} {
		if got := openAILanguage(input); got != want {
			t.Fatalf("openAILanguage(%q)=%q want %q", input, got, want)
		}
	}
	if _, ok := findSubtitleStream([]media.SubtitleStream{{Language: "pt"}}, "pt-PT"); !ok {
		t.Fatal("expected matching subtitle stream")
	}
	if _, ok := findSubtitleStream([]media.SubtitleStream{{Language: "es"}}, "pt-PT"); ok {
		t.Fatal("expected no matching subtitle stream")
	}
	if remaining := subtractLanguages([]string{"en", "pt"}, map[string]string{"pt": "pt.srt"}); len(remaining) != 1 || remaining[0] != "en" {
		t.Fatalf("unexpected remaining languages %#v", remaining)
	}
	if fileExists("") {
		t.Fatal("empty path should not exist")
	}
}

type fakeInspector struct {
	audio     []media.AudioStream
	subtitles []media.SubtitleStream
	chunks    []media.Chunk
}

func (fakeInspector) CheckTools(context.Context) error { return nil }

func (fakeInspector) DurationMS(context.Context, string) (int, error) {
	return 10_000, nil
}

func (f fakeInspector) AudioStreams(context.Context, string) ([]media.AudioStream, error) {
	if len(f.audio) > 0 {
		return f.audio, nil
	}
	return []media.AudioStream{{Index: 0, Language: "en", Default: true}}, nil
}

func (f fakeInspector) SubtitleStreams(context.Context, string) ([]media.SubtitleStream, error) {
	return f.subtitles, nil
}

func (f fakeInspector) ExtractAudioChunks(context.Context, string, media.AudioStream, string, int, int64) ([]media.Chunk, func(), error) {
	if len(f.chunks) > 0 {
		return f.chunks, func() {}, nil
	}
	return []media.Chunk{{Path: "chunk.mp3", OffsetMS: 0, ChunkNumber: 1}}, func() {}, nil
}

func (fakeInspector) ExtractSubtitles(_ context.Context, _ string, outputs map[string]media.SubtitleStreamOutput) error {
	for _, output := range outputs {
		if err := os.WriteFile(output.Path, []byte("1\n00:00:01,000 --> 00:00:02,000\nEmbedded\n\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

type fakeSpeech struct {
	transcriptions         int
	translations           int
	transcriptionLanguages []string
	transcriptionOutputs   []string
}

func (f *fakeSpeech) TranscribeSRT(_ context.Context, _ string, _ string, _ string, language string) (string, error) {
	f.transcriptions++
	f.transcriptionLanguages = append(f.transcriptionLanguages, language)
	if len(f.transcriptionOutputs) >= f.transcriptions {
		return f.transcriptionOutputs[f.transcriptions-1], nil
	}
	return "1\n00:00:01,000 --> 00:00:02,000\nHello.\n\n", nil
}

func (f *fakeSpeech) TranslateCues(_ context.Context, cues []subtitle.Cue, _ string, _ string, _ string) ([]subtitle.Cue, error) {
	f.translations++
	out := make([]subtitle.Cue, len(cues))
	copy(out, cues)
	for idx := range out {
		out[idx].Text = "Ola."
	}
	return out, nil
}

func testApp(cfg config.Config) *App {
	if cfg.Subtitles.Output.Title == "" {
		cfg.Subtitles.Output.Title = "subtitler"
	}
	if cfg.OpenAI.MaxChunkSeconds == 0 {
		cfg.OpenAI.MaxChunkSeconds = 1200
	}
	if cfg.OpenAI.TranscriptionModel == "" {
		cfg.OpenAI.TranscriptionModel = "whisper-1"
	}
	if cfg.Processing.MaxAttempts == 0 {
		cfg.Processing.MaxAttempts = 3
	}
	if cfg.Processing.RetryFailedAfter == 0 {
		cfg.Processing.RetryFailedAfter = time.Hour
	}
	app := New(cfg, slog.Default(), nil, "test")
	app.inspector = fakeInspector{}
	app.openai = &fakeSpeech{}
	return app
}

func writeVideo(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Episode.mkv")
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func itemFor(path string) arr.MediaItem {
	return arr.MediaItem{Source: "test", Path: path, Title: "Episode", Context: "Episode"}
}
