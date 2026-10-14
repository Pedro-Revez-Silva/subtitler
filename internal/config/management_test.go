package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
		{"management:\n  verification_model: large\n", false},
		{"management:\n  verification_model: none\n", false},
		{"management:\n  verification_fallback_model: invalid\n", false},
		{"management:\n  paid_daily_usd: .nan\n", false},
		{"management:\n  paid_daily_usd: .inf\n", false},
		{"management:\n  paid_daily_usd: -1\n", false},
		{"management:\n  queues:\n    file_workers: 17\n", false},
		{"management:\n  queues:\n    transcription_workers: -1\n", false},
		{"management:\n  queues:\n    worker_threads: 17\n", false},
		{"management:\n  queues:\n    batch_size: -1\n", false},
		{"management:\n  queues:\n    poll_seconds: 61\n", false},
		{"management:\n  queues:\n    min_memory_gb: .inf\n", false},
		{"management:\n  queues:\n    max_load_per_cpu: 2\n", false},
		{"management:\n  queues:\n    max_io_pressure: -1\n", false},
		{"management:\n  queues:\n    adaptive: false\n    batch_size: 32\n    file_workers: 3\n    transcription_workers: 2\n    worker_threads: 1\n    poll_seconds: 2\n    min_memory_gb: 1\n    max_load_per_cpu: 0.8\n    max_io_pressure: 25\n  verify_words: true\n", true},
		{"management:\n  enabled: true\n  state_dir: /data/owner\n  quarantine_dir: /media/archive\n  verify_words: true\n  verification_model: small\n  verification_fallback_model: none\n", true},
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

func TestConfiguredSubtitleLanguages(t *testing.T) {
	for _, tc := range []struct {
		values string
		want   []string
	}{
		{"[fr, es, de]", []string{"fr", "es", "de"}},
		{"[pt_BR, en-us, eng, EN]", []string{"pt-BR", "en-US", "en"}},
		{"[zh-Hant-TW]", []string{"zh-Hant-TW"}},
		{"[auto]", nil}, {"[xx]", nil}, {"['../en']", nil}, {"['']", nil}, {"[]", nil},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("subtitles:\n  required_languages: "+tc.values+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if tc.want == nil {
			if err == nil {
				t.Fatalf("accepted invalid language settings %s", tc.values)
			}
		} else if err != nil || !reflect.DeepEqual(cfg.Subtitles.RequiredLanguages, tc.want) {
			t.Fatalf("languages %s = %v, %v; want %v", tc.values, cfg.Subtitles.RequiredLanguages, err, tc.want)
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
