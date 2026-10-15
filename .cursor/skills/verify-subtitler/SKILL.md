---
name: verify-subtitler
description: Drive the Subtitler CLI from an isolated work directory. Use when proving inspect, doctor, generate, scan, or status against the built binary and a synthetic tone file.
---

# Verify the Subtitler CLI

The user runs the `subtitler` CLI. Build it in an empty work directory, then drive one command at a time. Unit tests call libraries and fakes. They do not prove this CLI.

Read `features/README.md` before a drive. Each feature file is the recipe for that command.

## Launch

Launch builds the binary. The CLI exits when the command finishes. There is no server process to leave running.

1. Create an empty directory outside the repo root. Export its path as `WORK`. A second run needs its own `WORK` so the two state files stay apart.
2. Export `GOTOOLCHAIN=go1.26.9` and `SUBTITLER_TELEMETRY=off`. Leave `OPENAI_API_KEY` unset. Go must be able to fetch `go1.26.9`. Do not `pip install` `manager/requirements.txt`.
3. From the repo root, run `go build -o "$WORK/subtitler" ./cmd/subtitler`.
4. Ready means `"$WORK/subtitler" version` prints a version and exits 0.
5. Confirm `ffmpeg` and `ffprobe` resolve on `PATH`.
6. Run `.cursor/skills/verify-subtitler/scripts/tone-mkv.sh` with `WORK` set. The script writes `$WORK/tone.mkv`.
7. Write `$WORK/config.yaml` with only these lines.

```yaml
dry_run: true
telemetry:
  enabled: false
```

Do not copy `config.example.yaml`. Do not set a Sonarr URL or a Radarr URL.

Run every later command with the current directory set to `$WORK`. The default state path is `subtitler-state.json` in the current directory. The repo root must not gain that file.

## Doctor

Run doctor after Launch, and again before a drive when a command has failed.

```bash
cd "$WORK"
"$WORK/subtitler" doctor -config "$WORK/config.yaml"
```

The instance is worth driving when the exit code is 0 and stdout contains `media tools available`. The empty-key warning is expected. It means generate cannot run. Doctor needs `$WORK/config.yaml` to exist. Also require `"$WORK/subtitler" version` to exit 0.

## Drive

Deliver input through `control-cli` from `cursor-team-kit`. `control-cli` is not in this repo. When it is absent, run the `subtitler` command below. Judge the run by that command's exit code and stdout.

The offline command that finishes is below.

```bash
cd "$WORK"
"$WORK/subtitler" inspect -config "$WORK/config.yaml" "$WORK/tone.mkv"
```

Pass when the exit code is 0 and stdout contains `audio stream` and `codec=aac`.

Errors from this CLI are slog lines on stdout. Read-only diagnostics are the exit code, stdout, stderr, and `find` of `$WORK`.

## Evidence

Save proof under `/tmp/verify-subtitler-evidence`. That directory is outside `$WORK` and outside cleanup.

For each command, save the command, the exit code, stdout, stderr, and the `find` listing of `$WORK`.

Drive the real binary. Do not call a library function or a test fake in its place. For generate, also save `subtitler-state.json` and any `.srt` beside the tone file. With no API key, a passing generate proof is exit code 1, the API-key error on stdout, and no `.srt`.

## Cleanup

The CLI exits on its own. Do not kill by process name.

Delete these paths when they exist.

- `$WORK/tone.mkv`
- `$WORK/config.yaml`
- `$WORK/subtitler`
- `$WORK/subtitler-state.json`

Leave `/tmp/verify-subtitler-evidence` in place. A cleanup that deletes that directory fails the proof.

## Helpers

`scripts/tone-mkv.sh` is executable. From the repo root, with `WORK` set to the empty work directory, run the script.

```bash
.cursor/skills/verify-subtitler/scripts/tone-mkv.sh
```

The script runs ffmpeg and writes `$WORK/tone.mkv`. `ffmpeg` must resolve on `PATH`.

## Keep the map current

Use `/maintain-verification-skill` when the CLI changes. This skill states no cadence.
