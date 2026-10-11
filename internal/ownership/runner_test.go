package ownership

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pedro-Revez-Silva/subtitler/internal/config"
)

func TestRunnerDoesNotSendCredentialsToStatusOrDryRun(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	script := filepath.Join(t.TempDir(), "check.py")
	if err := os.WriteFile(script, []byte("import json,sys\nr=json.load(sys.stdin)\nassert 'openai' not in r\nprint(r['action'])\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Management.Script = script
	cfg.Management.PaidFallback = true
	for _, action := range []string{"status", "scan"} {
		cfg.DryRun = true
		var out bytes.Buffer
		if err := Run(context.Background(), cfg, action, nil, nil, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), action) {
			t.Fatal("lost output")
		}
	}
}

func TestRunnerDoesNotExposeChildDiagnostics(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	script := filepath.Join(t.TempDir(), "fail.py")
	if err := os.WriteFile(script, []byte("import sys\nprint('private diagnostic',file=sys.stderr)\nsys.exit(1)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Management.Script = script
	err := Run(context.Background(), cfg, "scan", nil, nil, &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), "private diagnostic") {
		t.Fatal("child diagnostics exposed")
	}
}
