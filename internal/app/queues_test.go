package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Pedro-Revez-Silva/subtitler/internal/config"
)

func TestGenerationQueueRunsWhileInventoryIsBlocked(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	root := t.TempDir()
	marker := filepath.Join(root, "generation-started")
	script := filepath.Join(root, "worker.py")
	if err := os.WriteFile(script, []byte("import json,sys,time\nfrom pathlib import Path\nr=json.load(sys.stdin)\nassert r['action']=='transcription'\nPath("+"'"+marker+"'"+").touch()\ntime.sleep(60)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Management.Enabled = true
	cfg.Management.Script = script
	cfg.Radarr.URL = server.URL
	cfg.Radarr.APIKey = "unit-test-placeholder"
	service := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, "test")
	done := make(chan struct{})
	go func() { service.RunOwnershipDaemon(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		cancel()
		t.Fatal("generation queue was blocked by inventory")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("queue workers did not stop on cancellation")
	}
}
