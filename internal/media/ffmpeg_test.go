package media

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectAudioStreamPrefersRequestedNonCommentary(t *testing.T) {
	streams := []AudioStream{
		{Index: 0, Language: "en", Title: "Director Commentary", Default: true},
		{Index: 1, Language: "pt", Title: "Main", Default: false},
		{Index: 2, Language: "en", Title: "Main", Default: false},
	}
	stream, err := SelectAudioStream(streams, "eng")
	if err != nil {
		t.Fatal(err)
	}
	if stream.Index != 2 {
		t.Fatalf("expected non-commentary English stream, got %#v", stream)
	}
}

func TestSelectAudioStreamByPreferenceUsesFirstMatchingLanguage(t *testing.T) {
	streams := []AudioStream{
		{Index: 0, Language: "pt", Title: "Main", Default: true},
		{Index: 1, Language: "en", Title: "Main", Default: false},
		{Index: 2, Language: "es", Title: "Main", Default: false},
	}
	stream, err := SelectAudioStreamByPreference(streams, []string{"en", "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if stream.Index != 1 {
		t.Fatalf("expected English preference to beat default stream, got %#v", stream)
	}
}

func TestSelectAudioStreamFallbacks(t *testing.T) {
	if _, err := SelectAudioStream(nil, "auto"); err == nil {
		t.Fatal("expected empty stream error")
	}
	streams := []AudioStream{
		{Index: 0, Language: "en", Title: "Director Commentary", Default: true},
		{Index: 1, Language: "es", Title: "Main", Default: true},
	}
	stream, err := SelectAudioStream(streams, "pt")
	if err != nil {
		t.Fatal(err)
	}
	if stream.Index != 1 {
		t.Fatalf("expected default non-commentary fallback, got %#v", stream)
	}

	stream, err = SelectAudioStream([]AudioStream{{Index: 3, Title: "Director Commentary"}}, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if stream.Index != 3 {
		t.Fatalf("expected final fallback, got %#v", stream)
	}
}

func TestNormalizeLanguage(t *testing.T) {
	tests := map[string]string{
		" eng ": "en",
		"chi":   "zh",
		"zho":   "zh",
		"cmn":   "zh",
		"por":   "pt",
		"pt_br": "pt-BR",
		"pt-pt": "pt-PT",
		"jpn":   "jpn",
	}
	for input, want := range tests {
		if got := normalizeLanguage(input); got != want {
			t.Fatalf("normalizeLanguage(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCleanExtractedSRT(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub.srt")
	input := `<font color="red">{\an8}<b><i>Hello | world</i></b></font>`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CleanExtractedSRT(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	for _, unwanted := range []string{"font", "{\\an8}", "<b>", "<i>", " | "} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("expected cleaned output, got %q", output)
		}
	}
	if !strings.Contains(output, "Hello\nworld") {
		t.Fatalf("expected line break cleanup, got %q", output)
	}
}

func TestInspectorUsesConfiguredToolPaths(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	inspector := Inspector{FFmpegPath: tool, FFprobePath: tool}
	if inspector.ffmpeg() != tool || inspector.ffprobe() != tool {
		t.Fatal("expected configured tool paths")
	}
	if err := inspector.CheckTools(context.Background()); err != nil {
		t.Fatal(err)
	}

	bad := Inspector{FFmpegPath: filepath.Join(dir, "missing"), FFprobePath: tool}
	if err := bad.CheckTools(context.Background()); err == nil {
		t.Fatal("expected missing ffmpeg error")
	}
}

func TestInspectorDurationAndStreamsUseFFprobeJSON(t *testing.T) {
	dir := t.TempDir()
	ffprobe := filepath.Join(dir, "ffprobe")
	script := `#!/bin/sh
case "$*" in
  *format=duration*) echo "12.345" ;;
  *-show_streams*) cat <<'JSON'
{"streams":[
  {"index":0,"codec_name":"aac","codec_type":"audio","disposition":{"default":1},"tags":{"language":"eng","title":"Main"}},
  {"index":1,"codec_name":"subrip","codec_type":"subtitle","disposition":{"default":0},"tags":{"language":"por","title":"Portuguese"}}
]}
JSON
  ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(ffprobe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	inspector := Inspector{FFprobePath: ffprobe}
	duration, err := inspector.DurationMS(context.Background(), "video.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if duration != 12_345 {
		t.Fatalf("unexpected duration %d", duration)
	}
	audio, err := inspector.AudioStreams(context.Background(), "video.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if len(audio) != 1 || audio[0].Language != "en" || !audio[0].Default {
		t.Fatalf("unexpected audio streams %#v", audio)
	}
	subs, err := inspector.SubtitleStreams(context.Background(), "video.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Language != "pt" {
		t.Fatalf("unexpected subtitle streams %#v", subs)
	}
}

func TestInspectorProbeErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho nope >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	inspector := Inspector{FFprobePath: bad}
	if _, err := inspector.DurationMS(context.Background(), "video.mkv"); err == nil {
		t.Fatal("expected duration error")
	}
	if _, err := inspector.AudioStreams(context.Background(), "video.mkv"); err == nil {
		t.Fatal("expected stream error")
	}

	invalid := filepath.Join(dir, "invalid")
	if err := os.WriteFile(invalid, []byte("#!/bin/sh\necho '{'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	inspector = Inspector{FFprobePath: invalid}
	if _, err := inspector.SubtitleStreams(context.Background(), "video.mkv"); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestExtractAudioChunksUsesFFmpegSegments(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	script := `#!/bin/sh
pattern="${@: -1}"
dir="$(dirname "$pattern")"
touch "$dir/chunk_00000.mp3" "$dir/chunk_00001.mp3"
`
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/bash\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	inspector := Inspector{FFmpegPath: ffmpeg}
	chunks, cleanup, err := inspector.ExtractAudioChunks(context.Background(), "video.mkv", AudioStream{Index: 2}, t.TempDir(), 20)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(chunks) != 2 || chunks[1].OffsetMS != 20_000 || chunks[1].ChunkNumber != 2 {
		t.Fatalf("unexpected chunks %#v", chunks)
	}
}

func TestExtractSubtitlesWritesAndCleansOutputs(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	script := `#!/bin/sh
last=""
for arg in "$@"; do last="$arg"; done
echo '<b>Hello | world</b>' > "$last"
`
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	inspector := Inspector{FFmpegPath: ffmpeg}
	outputPath := filepath.Join(dir, "out.srt")
	err := inspector.ExtractSubtitles(context.Background(), "video.mkv", map[string]SubtitleStreamOutput{
		"en": {Stream: SubtitleStream{Index: 3}, Path: outputPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "Hello\nworld\n" {
		t.Fatalf("unexpected cleaned subtitle %q", string(data))
	}
	if err := inspector.ExtractSubtitles(context.Background(), "video.mkv", nil); err != nil {
		t.Fatal(err)
	}
}
