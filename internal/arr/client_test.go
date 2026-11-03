package arr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSystemStatusSendsAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/system/status" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("X-Api-Key"); got != "test-key" {
			t.Fatalf("expected API key header, got %q", got)
		}
		if err := json.NewEncoder(w).Encode(systemStatus{Version: "1.0.0"}); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	client := New(server.URL, "test-key")
	if err := client.SystemStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSystemStatusReportsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer server.Close()

	client := New(server.URL, "bad-key")
	if err := client.SystemStatus(context.Background()); err == nil {
		t.Fatal("expected non-2xx status to return an error")
	}
}

func TestRadarrMoviesBuildsMediaItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/movie" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]radarrMovie{
			{ID: 1, Title: "With File", Year: 2024, Path: "/movies/with"},
			{ID: 2, Title: "Missing File", Year: 2024, Path: "/movies/missing"},
			{ID: 3, Title: "Relative File", Year: 2025, Path: "/movies/relative"},
		})
	}))
	defer server.Close()

	client := New(server.URL, "test-key")
	movies, err := client.RadarrMovies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 0 {
		t.Fatalf("expected empty because test data has no movie file ids, got %#v", movies)
	}
}

func TestRadarrMoviesUsesAbsoluteAndRelativePaths(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"title":"Absolute","year":2024,"path":"/movies/absolute","movieFile":{"id":11,"path":"/media/Absolute/movie.mkv"}},
			{"id":2,"title":"Relative","year":2025,"path":"/movies/relative","movieFile":{"id":22,"relativePath":"Relative/movie.mkv"}}
		]`))
	}))
	defer server.Close()

	items, err := New(server.URL, "test-key").RadarrMovies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %#v", items)
	}
	if items[0].Path != "/media/Absolute/movie.mkv" || items[1].Path != "/movies/relative/Relative/movie.mkv" {
		t.Fatalf("unexpected paths %#v", items)
	}
	if items[0].Context != "Absolute (2024)" {
		t.Fatalf("unexpected context %q", items[0].Context)
	}
}

func TestSonarrEpisodesBuildsMediaItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/series":
			_, _ = w.Write([]byte(`[{"id":7,"title":"Show","path":"/shows/Show","added":"2020-01-01T00:00:00Z"}]`))
		case "/api/v3/episodefile":
			if got := r.URL.Query().Get("seriesId"); got != "7" {
				t.Fatalf("unexpected series id %q", got)
			}
			_, _ = w.Write([]byte(`[
				{"id":1,"path":"/media/Show/S01E01.mkv","dateAdded":"2026-11-03T12:30:00Z"},
				{"id":2,"relativePath":"Season 1/S01E02.mkv"},
				{"id":3}
			]`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	items, err := New(server.URL, "test-key").SonarrEpisodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %#v", items)
	}
	if items[0].Source != "sonarr" || items[1].Path != "/shows/Show/Season 1/S01E02.mkv" {
		t.Fatalf("unexpected sonarr items %#v", items)
	}
	if items[0].AddedAt != time.Date(2026, 11, 3, 12, 30, 0, 0, time.UTC).Unix() ||
		items[1].AddedAt != time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("new episode import dates must take precedence over the older series date: %#v", items)
	}
}

func TestRadarrLibraryImportDates(t *testing.T) {
	imported := time.Date(2026, 11, 3, 12, 30, 0, 0, time.UTC).Unix()
	requested := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	for _, tc := range []struct {
		name, fileDate, titleDate string
		want                      int64
	}{
		{"new import of an older title", "2026-11-03T12:30:00.1234567Z", "2020-01-01T00:00:00Z", imported},
		{"timezone offset", "2026-11-03T14:30:00+02:00", "2020-01-01T00:00:00Z", imported},
		{"missing file date", "", "2020-01-01T00:00:00Z", requested},
		{"invalid file date", "not-a-date", "2020-01-01T00:00:00Z", requested},
		{"zero file date", "0001-01-01T00:00:00Z", "2020-01-01T00:00:00Z", requested},
		{"unknown dates", "not-a-date", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode([]map[string]any{{
					"id": 1, "title": "Movie", "added": tc.titleDate,
					"movieFile": map[string]any{"id": 2, "path": "/movies/movie.mkv", "dateAdded": tc.fileDate},
				}})
			}))
			defer server.Close()
			items, err := New(server.URL, "test-key").RadarrMovies(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 || items[0].AddedAt != tc.want {
				t.Fatalf("expected library import time %d, got %#v", tc.want, items)
			}
		})
	}
}
