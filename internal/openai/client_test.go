package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Pedro-Revez-Silva/subtitler/internal/subtitle"
)

func TestTranscribeSRTPostsMultipartRequest(t *testing.T) {
	audioPath := filepath.Join(t.TempDir(), "sample.mp3")
	if err := os.WriteFile(audioPath, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected auth header %q", got)
		}
		if err := r.ParseMultipartForm(1024 * 1024); err != nil {
			t.Fatal(err)
		}
		assertFormValue(t, r, "model", "whisper-1")
		assertFormValue(t, r, "response_format", "srt")
		assertFormValue(t, r, "prompt", "Movie context")
		assertFormValue(t, r, "language", "en")
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "audio" {
			t.Fatalf("unexpected file body %q", string(data))
		}
		_, _ = w.Write([]byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n\n"))
	}))
	defer server.Close()

	client := New("test-key", server.URL+"/")
	srt, err := client.TranscribeSRT(context.Background(), audioPath, "whisper-1", "Movie context", "en")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(srt, "Hello") {
		t.Fatalf("unexpected SRT %q", srt)
	}
}

func TestTranscribeSRTRejectsUnsupportedModelAndHTTPError(t *testing.T) {
	client := New("test-key", "http://example.invalid")
	if _, err := client.TranscribeSRT(context.Background(), "missing.mp3", "gpt-4o-transcribe", "", ""); err == nil {
		t.Fatal("expected unsupported model error")
	}

	audioPath := filepath.Join(t.TempDir(), "sample.mp3")
	if err := os.WriteFile(audioPath, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	client = New("test-key", server.URL)
	if _, err := client.TranscribeSRT(context.Background(), audioPath, "whisper-1", "", "auto"); err == nil {
		t.Fatal("expected HTTP error")
	}
}

func TestTranslateSRTReadsOutputTextAndFallbackContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "gpt-test" {
			t.Fatalf("unexpected model %#v", payload["model"])
		}
		if !strings.Contains(payload["input"].(string), "Translate this SRT") {
			t.Fatalf("unexpected prompt %q", payload["input"])
		}
		_ = json.NewEncoder(w).Encode(responsesResponse{OutputText: "translated"})
	}))
	defer server.Close()

	client := New("test-key", server.URL)
	text, err := client.TranslateSRT(context.Background(), "1\n...", "gpt-test", "pt", "context")
	if err != nil {
		t.Fatal(err)
	}
	if text != "translated" {
		t.Fatalf("unexpected text %q", text)
	}
}

func TestTranslateCuesBatchesAndCleansJSON(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		start := strings.Index(payload["input"], "Input JSON:\n")
		if start < 0 {
			t.Fatalf("input JSON not found in prompt %q", payload["input"])
		}
		inputJSON := strings.TrimSpace(payload["input"][start+len("Input JSON:\n"):])
		var items []cueTranslationItem
		if err := json.Unmarshal([]byte(inputJSON), &items); err != nil {
			t.Fatal(err)
		}
		translations := make([]cueTranslationItem, 0, len(items))
		for _, item := range items {
			translations = append(translations, cueTranslationItem{ID: item.ID, Text: "pt " + item.Text})
		}
		body, err := json.Marshal(cueTranslationResponse{Translations: translations})
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(responsesResponse{OutputText: "```json\n" + string(body) + "\n```"})
	}))
	defer server.Close()

	cues := make([]subtitle.Cue, 21)
	for idx := range cues {
		cues[idx] = subtitle.Cue{Index: idx + 1, StartMS: idx * 1000, EndMS: idx*1000 + 500, Text: "cue"}
	}
	client := New("test-key", server.URL)
	translated, err := client.TranslateCues(context.Background(), cues, "gpt-test", "pt", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(translated) != 21 || translated[20].Text != "pt cue" {
		t.Fatalf("unexpected translations %#v", translated)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected two batches, got %d", calls.Load())
	}
}

func TestTranslateCuesReturnsShapeErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(responsesResponse{OutputText: `{"translations":[]}`})
	}))
	defer server.Close()

	client := New("test-key", server.URL)
	_, err := client.TranslateCues(context.Background(), []subtitle.Cue{{Index: 1, Text: "hi"}}, "gpt-test", "pt", "")
	if err == nil {
		t.Fatal("expected missing translation error")
	}
}

func TestResponseTextFallbacksAndCleanJSON(t *testing.T) {
	data := []byte(`{"output":[{"content":[{"text":"fallback"}]}]}`)
	text, err := responseText(data)
	if err != nil {
		t.Fatal(err)
	}
	if text != "fallback" {
		t.Fatalf("unexpected fallback %q", text)
	}
	if cleanJSON("```json\n{\"ok\":true}\n```") != "{\"ok\":true}" {
		t.Fatal("expected fenced JSON to be cleaned")
	}
	if _, err := responseText([]byte(`{}`)); err == nil {
		t.Fatal("expected missing output text error")
	}
}

func assertFormValue(t *testing.T, r *http.Request, key, want string) {
	t.Helper()
	if got := r.FormValue(key); got != want {
		t.Fatalf("expected form %s=%q, got %q", key, want, got)
	}
}
