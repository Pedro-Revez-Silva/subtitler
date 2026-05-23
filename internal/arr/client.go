package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

type MediaItem struct {
	Source  string
	ID      int
	Title   string
	Path    string
	Context string
}

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) RadarrMovies(ctx context.Context) ([]MediaItem, error) {
	var movies []radarrMovie
	if err := c.get(ctx, "/api/v3/movie", nil, &movies); err != nil {
		return nil, err
	}

	items := make([]MediaItem, 0, len(movies))
	for _, movie := range movies {
		if movie.MovieFile.ID == 0 {
			continue
		}
		path := movie.MovieFile.Path
		if path == "" && movie.MovieFile.RelativePath != "" {
			path = filepath.Join(movie.Path, movie.MovieFile.RelativePath)
		}
		if path == "" {
			continue
		}
		items = append(items, MediaItem{
			Source:  "radarr",
			ID:      movie.ID,
			Title:   movie.Title,
			Path:    path,
			Context: fmt.Sprintf("%s (%d)", movie.Title, movie.Year),
		})
	}
	return items, nil
}

func (c *Client) SonarrEpisodes(ctx context.Context) ([]MediaItem, error) {
	var series []sonarrSeries
	if err := c.get(ctx, "/api/v3/series", nil, &series); err != nil {
		return nil, err
	}

	items := []MediaItem{}
	for _, show := range series {
		values := url.Values{}
		values.Set("seriesId", fmt.Sprint(show.ID))
		var files []sonarrEpisodeFile
		if err := c.get(ctx, "/api/v3/episodefile", values, &files); err != nil {
			return nil, err
		}
		for _, file := range files {
			path := file.Path
			if path == "" && file.RelativePath != "" {
				path = filepath.Join(show.Path, file.RelativePath)
			}
			if path == "" {
				continue
			}
			items = append(items, MediaItem{
				Source:  "sonarr",
				ID:      file.ID,
				Title:   show.Title,
				Path:    path,
				Context: show.Title,
			})
		}
	}
	return items, nil
}

func (c *Client) get(ctx context.Context, endpoint string, query url.Values, out any) error {
	u, err := url.Parse(c.baseURL + endpoint)
	if err != nil {
		return err
	}
	if query != nil {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s returned %s", u.String(), resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type radarrMovie struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
	Path      string `json:"path"`
	MovieFile struct {
		ID           int    `json:"id"`
		Path         string `json:"path"`
		RelativePath string `json:"relativePath"`
	} `json:"movieFile"`
}

type sonarrSeries struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Path  string `json:"path"`
}

type sonarrEpisodeFile struct {
	ID           int    `json:"id"`
	Path         string `json:"path"`
	RelativePath string `json:"relativePath"`
}
