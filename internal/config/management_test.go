package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestManagementConfiguration(t *testing.T) {
	for _, tc := range []struct {
		yaml  string
		valid bool
	}{
		{"management:\n  enabled: true\n", false},
		{"management:\n  min_free_gb: -1\n", false},
		{"management:\n  paid_fallback: true\n", false},
		{"management:\n  enabled: true\n  state_dir: /data/owner\n  quarantine_dir: /media/archive\n  verify_words: true\n", true},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if (err == nil) != tc.valid {
			t.Fatalf("valid=%v error=%v", tc.valid, err)
		}
		if tc.valid && !cfg.Management.VerifyWords {
			t.Fatal("lost word verification setting")
		}
	}
}

func TestLegacyAudioCleanupSettingIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("management:\n  prune_audio: true\n  prune_embedded: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg.Management)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["prune_audio"]; exists || !cfg.Management.PruneEmbedded {
		t.Fatalf("legacy audio policy is still active or subtitle cleanup was lost: %s", data)
	}
}
