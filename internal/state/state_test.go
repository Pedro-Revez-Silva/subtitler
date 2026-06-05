package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreSaveOpenPutGetAndInstallationID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.InstallationID()
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected installation id")
	}
	if store.TelemetryInstalledSent() {
		t.Fatal("expected telemetry install marker to start unset")
	}
	store.MarkTelemetryInstalledSent()
	store.Put("/media/movie.mkv", FileState{Source: "radarr"})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected private state file permissions, got %o", info.Mode().Perm())
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopenedID, err := reopened.InstallationID(); err != nil || reopenedID != id {
		t.Fatalf("expected persisted installation id %q, got %q err=%v", id, reopenedID, err)
	}
	if !reopened.TelemetryInstalledSent() {
		t.Fatal("expected persisted telemetry install marker")
	}
	fileState, ok := reopened.Get("/media/movie.mkv")
	if !ok || fileState.Path != "/media/movie.mkv" || fileState.Source != "radarr" {
		t.Fatalf("unexpected file state ok=%v state=%#v", ok, fileState)
	}
}

func TestOpenInitializesMissingFilesMapAndRejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"installation_id":"id"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Put("file.mkv", FileState{})
	if _, ok := store.Get("file.mkv"); !ok {
		t.Fatal("expected files map to be initialized")
	}

	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, []byte(`not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(badPath); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}
