package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportsSelectAConfiguredLanguage(t *testing.T) {
	for _, tc := range []struct {
		languages string
		flag      string
		want      string
		errorText string
	}{
		{"[fr]", "", "fr", ""},
		{"[fr, es]", "ES", "es", ""},
		{"[fr, es]", "", "", "requires -lang"},
		{"[fr]", "en", "", "must be one of"},
	} {
		dir := t.TempDir()
		request := filepath.Join(dir, "request.json")
		t.Setenv("SUBTITLER_TEST_REQUEST", request)
		python := filepath.Join(dir, "capture-python")
		if err := os.WriteFile(python, []byte("#!/bin/sh\ncat > \"$SUBTITLER_TEST_REQUEST\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(dir, "config.yaml")
		config := "subtitles:\n  required_languages: " + tc.languages + "\nmanagement:\n  enabled: true\n  state_dir: " + dir + "\n  quarantine_dir: " + dir + "\n  python: " + python + "\n"
		if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		args := []string{"-config", configPath}
		if tc.flag != "" {
			args = append(args, "-lang", tc.flag)
		}
		args = append(args, "Film")
		err := runManagement(context.Background(), "report", args)
		if tc.errorText != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errorText) {
				t.Fatalf("expected %q, got %v", tc.errorText, err)
			}
			if _, err := os.Stat(request); !os.IsNotExist(err) {
				t.Fatal("invalid report reached the ownership engine")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(request)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Feedback map[string]string `json:"feedback"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Feedback["language"] != tc.want {
			t.Fatalf("wrong report language: %s", data)
		}
	}
}
