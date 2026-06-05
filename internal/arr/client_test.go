package arr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
			_, _ = w.Write([]byte(`[{"id":7,"title":"Show","path":"/shows/Show"}]`))
		case "/api/v3/episodefile":
			if got := r.URL.Query().Get("seriesId"); got != "7" {
				t.Fatalf("unexpected series id %q", got)
			}
			_, _ = w.Write([]byte(`[
				{"id":1,"path":"/media/Show/S01E01.mkv"},
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
}
