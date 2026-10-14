package ownership

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"

	"github.com/Pedro-Revez-Silva/subtitler/internal/arr"
	"github.com/Pedro-Revez-Silva/subtitler/internal/config"
)

func Run(ctx context.Context, cfg config.Config, action string, items []arr.MediaItem, feedback map[string]string, output io.Writer) error {
	python, script := cfg.Management.Python, cfg.Management.Script
	if python == "" {
		python = "python3"
	}
	if script == "" {
		script = "/opt/subtitler/manager.py"
	}
	request := map[string]any{
		"action": action, "management": cfg.Management, "items": items,
		"languages": cfg.Subtitles.RequiredLanguages, "temp_dir": cfg.TempDir,
		"dry_run": cfg.DryRun, "max_jobs": cfg.Processing.MaxJobsPerScan,
		"retry_seconds": cfg.Processing.RetryFailedAfter.Seconds(),
		"ffmpeg":        cfg.Tools.FFmpeg, "ffprobe": cfg.Tools.FFprobe, "feedback": feedback,
	}
	if (action == "scan" || action == "files" || action == "transcription") && !cfg.DryRun {
		request["arr"] = map[string]any{
			"radarr": map[string]string{"url": cfg.Radarr.URL, "api_key": cfg.Radarr.APIKey},
			"sonarr": map[string]string{"url": cfg.Sonarr.URL, "api_key": cfg.Sonarr.APIKey},
		}
	}
	if cfg.Management.PaidFallback && (action == "scan" || action == "transcription") && !cfg.DryRun {
		request["openai"] = map[string]string{"api_key": cfg.OpenAI.APIKey, "base_url": cfg.OpenAI.BaseURL,
			"transcription_model": cfg.OpenAI.TranscriptionModel, "translation_model": cfg.OpenAI.TranslationModel}
	}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, python, script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = output
	// The engine emits sanitized status records. Provider exceptions and runtime
	// configuration (which may contain credentials) never enter daemon logs.
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("subtitle owner failed: %w", err)
	}
	return nil
}
