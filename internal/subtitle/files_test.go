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
