package subtitle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindSidecarsMatchesMediaBasename(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Movie Name (2024).mkv")
	files := []string{
		video,
		filepath.Join(dir, "Movie Name (2024).en.srt"),
		filepath.Join(dir, "Movie Name (2024).pt-PT.srt"),
		filepath.Join(dir, "Movie Name (2024).forced.srt"),
		filepath.Join(dir, "Other Movie.en.srt"),
	}
	for _, file := range files {
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sidecars, err := FindSidecars(video)
	if err != nil {
		t.Fatal(err)
	}
	if len(sidecars) != 3 {
		t.Fatalf("expected 3 matching sidecars, got %d: %#v", len(sidecars), sidecars)
	}

	var protected bool
	for _, sidecar := range sidecars {
		if IsProtected(sidecar, []string{"forced"}) {
			protected = true
		}
	}
	if !protected {
		t.Fatal("expected forced subtitle to be protected")
	}
}

func TestOutputPathIncludesTitleAndJellyfinLanguage(t *testing.T) {
	path := OutputPath("/media/Show/Episode.mkv", "pt-PT", "Subtitler")
	want := "/media/Show/Episode.subtitler.pt.srt"
	if path != want {
		t.Fatalf("expected %q, got %q", want, path)
	}
}

func TestOutputPathSanitizesTitleAndLanguageFallback(t *testing.T) {
	path := OutputPath("/media/Show/Episode.mkv", "es-MX", ` My: Bad/Title? `)
	want := "/media/Show/Episode.my-bad-title.es.srt"
	if path != want {
		t.Fatalf("expected %q, got %q", want, path)
	}

	path = OutputPath("/media/Show/Episode.mkv", "jpn", "")
	want = "/media/Show/Episode.jpn.srt"
	if path != want {
		t.Fatalf("expected %q, got %q", want, path)
	}
}

func TestNormalizeLanguageVariants(t *testing.T) {
	tests := map[string]string{
		"eng":        "en",
		"English":    "en",
		"por":        "pt",
		"pt_br":      "pt-BR",
		"pt-pt":      "pt-PT",
		"spa":        "es",
		"fra":        "fr",
		"deu":        "de",
		"it":         "it",
		"not-a-lang": "",
	}
	for input, want := range tests {
		if got := normalizeLanguage(input); got != want {
			t.Fatalf("normalizeLanguage(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFindSidecarsHandlesReadDirError(t *testing.T) {
	if _, err := FindSidecars(filepath.Join(t.TempDir(), "missing", "Movie.mkv")); err == nil {
		t.Fatal("expected missing directory error")
	}
}
