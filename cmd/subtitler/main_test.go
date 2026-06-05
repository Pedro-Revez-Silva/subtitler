package main

import (
	"flag"
	"path/filepath"
	"testing"
)

func TestLoadGenerateConfigUsesDefaultsWhenDefaultConfigMissing(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key")
	cfg, err := loadGenerateConfig(filepath.Join(t.TempDir(), "config.yaml"), false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpenAI.APIKey != "env-key" || cfg.OpenAI.TranscriptionModel != "whisper-1" {
		t.Fatalf("unexpected default config %#v", cfg.OpenAI)
	}
}

func TestLoadGenerateConfigErrorsWhenExplicitConfigMissing(t *testing.T) {
	if _, err := loadGenerateConfig(filepath.Join(t.TempDir(), "missing.yaml"), true); err == nil {
		t.Fatal("expected explicit missing config to fail")
	}
}

func TestFlagWasPassed(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("config", "config.yaml", "")
	fs.String("langs", "", "")
	if err := fs.Parse([]string{"-config", "custom.yaml"}); err != nil {
		t.Fatal(err)
	}
	if !flagWasPassed(fs, "config") {
		t.Fatal("expected config flag to be detected")
	}
	if flagWasPassed(fs, "langs") {
		t.Fatal("did not expect langs flag to be detected")
	}
}
