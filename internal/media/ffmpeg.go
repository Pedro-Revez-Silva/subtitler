package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type AudioStream struct {
	Index    int
	Language string
	Title    string
	Default  bool
	Codec    string
}

type SubtitleStream struct {
	Index    int
	Language string
	Title    string
	Codec    string
}

type Inspector struct {
	Logger      *slog.Logger
	FFmpegPath  string
	FFprobePath string
}

var (
	fontTagRE     = regexp.MustCompile(`</?font[^>]*>`)
	assPositionRE = regexp.MustCompile(`\{\\an\d+\}`)
)

func (i Inspector) CheckTools(ctx context.Context) error {
	if err := i.checkTool(ctx, i.ffprobe(), "-version"); err != nil {
		return err
	}
	if err := i.checkTool(ctx, i.ffmpeg(), "-version"); err != nil {
		return err
	}
	return nil
}

func (i Inspector) DurationMS(ctx context.Context, videoPath string) (int, error) {
	cmd := exec.CommandContext(ctx, i.ffprobe(), "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", videoPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffprobe duration failed: %w: %s", err, stderr.String())
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(stdout.String()), 64)
	if err != nil {
		return 0, err
	}
	return int(seconds * 1000), nil
}

func (i Inspector) AudioStreams(ctx context.Context, videoPath string) ([]AudioStream, error) {
	cmd := exec.CommandContext(ctx, i.ffprobe(), "-v", "error", "-print_format", "json", "-show_streams", videoPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w: %s", err, stderr.String())
	}

	var probed ffprobeStreams
	if err := json.Unmarshal(stdout.Bytes(), &probed); err != nil {
		return nil, err
	}

	streams := []AudioStream{}
	for _, stream := range probed.Streams {
		if stream.CodecType != "audio" {
			continue
		}
		streams = append(streams, AudioStream{
			Index:    stream.Index,
			Language: normalizeLanguage(stream.Tags.Language),
			Title:    stream.Tags.Title,
			Default:  stream.Disposition.Default == 1,
			Codec:    stream.CodecName,
		})
	}
	return streams, nil
}

func (i Inspector) SubtitleStreams(ctx context.Context, videoPath string) ([]SubtitleStream, error) {
	cmd := exec.CommandContext(ctx, i.ffprobe(), "-v", "error", "-print_format", "json", "-show_streams", videoPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w: %s", err, stderr.String())
	}

	var probed ffprobeStreams
	if err := json.Unmarshal(stdout.Bytes(), &probed); err != nil {
		return nil, err
	}

	streams := []SubtitleStream{}
	for _, stream := range probed.Streams {
		if stream.CodecType != "subtitle" {
			continue
		}
		streams = append(streams, SubtitleStream{
			Index:    stream.Index,
			Language: normalizeLanguage(stream.Tags.Language),
			Title:    stream.Tags.Title,
			Codec:    stream.CodecName,
		})
	}
	return streams, nil
}

func SelectAudioStream(streams []AudioStream, requestedLanguage string) (AudioStream, error) {
	return SelectAudioStreamByPreference(streams, []string{requestedLanguage})
}

func SelectAudioStreamByPreference(streams []AudioStream, requestedLanguages []string) (AudioStream, error) {
	if len(streams) == 0 {
		return AudioStream{}, fmt.Errorf("no audio streams found")
	}

	for _, requestedLanguage := range requestedLanguages {
		requestedLanguage = normalizeLanguage(requestedLanguage)
		if requestedLanguage == "" || requestedLanguage == "auto" {
			continue
		}
		for _, stream := range streams {
			if stream.Language == requestedLanguage && !isCommentary(stream.Title) {
				return stream, nil
			}
		}
	}
	for _, stream := range streams {
		if stream.Default && !isCommentary(stream.Title) {
			return stream, nil
		}
	}
	for _, stream := range streams {
		if !isCommentary(stream.Title) {
			return stream, nil
		}
	}
	return streams[0], nil
}

func (i Inspector) ExtractAudioChunks(ctx context.Context, videoPath string, stream AudioStream, tempRoot string, chunkSeconds int) ([]Chunk, func(), error) {
	workDir, err := os.MkdirTemp(tempRoot, "subtitler-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		if err := os.RemoveAll(workDir); err != nil && i.Logger != nil {
			i.Logger.Warn("failed to remove temp directory", "path", workDir, "error", err)
		}
	}

	outputPattern := filepath.Join(workDir, "chunk_%05d.mp3")
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", videoPath,
		"-map", fmt.Sprintf("0:%d", stream.Index),
		"-vn",
		"-ac", "1",
		"-ar", "16000",
		"-codec:a", "libmp3lame",
		"-b:a", "64k",
		"-f", "segment",
		"-segment_time", fmt.Sprint(chunkSeconds),
		"-reset_timestamps", "1",
		outputPattern,
	}
	cmd := exec.CommandContext(ctx, i.ffmpeg(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("ffmpeg audio extraction failed: %w: %s", err, stderr.String())
	}

	paths, err := filepath.Glob(filepath.Join(workDir, "chunk_*.mp3"))
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	sort.Strings(paths)
	chunks := make([]Chunk, 0, len(paths))
	for idx, path := range paths {
		chunks = append(chunks, Chunk{
			Path:        path,
			OffsetMS:    idx * chunkSeconds * 1000,
			ChunkNumber: idx + 1,
		})
	}
	return chunks, cleanup, nil
}

func (i Inspector) ExtractSubtitle(ctx context.Context, videoPath string, stream SubtitleStream, outputPath string) error {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", videoPath,
		"-map", fmt.Sprintf("0:%d", stream.Index),
		outputPath,
	}
	cmd := exec.CommandContext(ctx, i.ffmpeg(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg subtitle extraction failed: %w: %s", err, stderr.String())
	}
	return CleanExtractedSRT(outputPath)
}

func (i Inspector) ExtractSubtitles(ctx context.Context, videoPath string, outputs map[string]SubtitleStreamOutput) error {
	if len(outputs) == 0 {
		return nil
	}
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", videoPath,
	}
	for _, output := range outputs {
		args = append(args, "-map", fmt.Sprintf("0:%d", output.Stream.Index), output.Path)
	}
	cmd := exec.CommandContext(ctx, i.ffmpeg(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg subtitle extraction failed: %w: %s", err, stderr.String())
	}
	for _, output := range outputs {
		if err := CleanExtractedSRT(output.Path); err != nil {
			return err
		}
	}
	return nil
}

type SubtitleStreamOutput struct {
	Stream SubtitleStream
	Path   string
}

type Chunk struct {
	Path        string
	OffsetMS    int
	ChunkNumber int
}

type ffprobeStreams struct {
	Streams []struct {
		Index       int    `json:"index"`
		CodecName   string `json:"codec_name"`
		CodecType   string `json:"codec_type"`
		Disposition struct {
			Default int `json:"default"`
		} `json:"disposition"`
		Tags struct {
			Language string `json:"language"`
			Title    string `json:"title"`
		} `json:"tags"`
	} `json:"streams"`
}

func normalizeLanguage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "eng":
		return "en"
	case "chi", "zho", "cmn":
		return "zh"
	case "por":
		return "pt"
	case "pt_br", "pt-br":
		return "pt-BR"
	case "pt_pt", "pt-pt":
		return "pt-PT"
	default:
		return value
	}
}

func CleanExtractedSRT(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	text = strings.ReplaceAll(text, " | ", "\n")
	text = fontTagRE.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "<b>", "")
	text = strings.ReplaceAll(text, "</b>", "")
	text = strings.ReplaceAll(text, "<i>", "")
	text = strings.ReplaceAll(text, "</i>", "")
	text = assPositionRE.ReplaceAllString(text, "")
	return os.WriteFile(path, []byte(text), 0o644)
}

func isCommentary(title string) bool {
	title = strings.ToLower(title)
	return strings.Contains(title, "commentary") || strings.Contains(title, "director")
}

func (i Inspector) ffmpeg() string {
	if i.FFmpegPath != "" {
		return i.FFmpegPath
	}
	return "ffmpeg"
}

func (i Inspector) ffprobe() string {
	if i.FFprobePath != "" {
		return i.FFprobePath
	}
	return "ffprobe"
}

func (i Inspector) checkTool(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s is required but not available: %w: %s", name, err, stderr.String())
	}
	return nil
}
