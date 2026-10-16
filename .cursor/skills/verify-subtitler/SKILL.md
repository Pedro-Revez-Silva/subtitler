---
name: verify-subtitler
description: Drive the Subtitler CLI from an isolated work directory. Use when proving inspect, doctor, generate, scan, or status against the built binary and a synthetic tone file.
---

# Verify the Subtitler CLI

The user runs the `subtitler` CLI. Build it in an empty work directory, then drive one command at a time. Unit tests call libraries and fakes. They do not prove this CLI.

Read `features/README.md` before a drive. Each feature file is the recipe for that command.

## Launch

Launch builds the binary. The CLI exits when the command finishes. There is no server process to leave running.

Every run gets a new `WORK` and a new `EVIDENCE`. Both live on disk, outside the repo root. `WORK` holds scratch that Cleanup deletes. `EVIDENCE` holds proof that Cleanup keeps. Do not put either under `/tmp` or `/dev/shm`.

1. From the repo root, create both directories and export three variables.

```bash
mkdir -p "${XDG_CACHE_HOME:-$HOME/.cache}/verify-subtitler"
WORK="$(mktemp -d "${XDG_CACHE_HOME:-$HOME/.cache}/verify-subtitler/work.XXXXXX")"
EVIDENCE="${XDG_STATE_HOME:-$HOME/.local/state}/verify-subtitler/evidence/$(date -u +%Y%m%dT%H%M%SZ)-${WORK##*.}"
CAPTURE="$PWD/.cursor/skills/verify-subtitler/scripts/capture.sh"
mkdir -p "$(dirname "$EVIDENCE")"
mkdir "$EVIDENCE"
export WORK EVIDENCE CAPTURE
```

Note the three values. Export the same values in every later shell. Running this block again starts a new run.

2. Export `GOTOOLCHAIN=go1.26.9` and `SUBTITLER_TELEMETRY=off`. Run `unset OPENAI_API_KEY`. Go must be able to fetch `go1.26.9`. Do not `pip install` `manager/requirements.txt`.
3. From the repo root, record the run. Set `DELIVERY` to `control-cli` or `direct`, as described in Drive.

```bash
DELIVERY=direct
printf 'work=%s\ncommit=%s\ndelivery=%s\n' "$WORK" "$(git describe --always --dirty --abbrev=40)" "$DELIVERY" >"$EVIDENCE/run.txt"
```

4. From the repo root, run `go build -o "$WORK/subtitler" ./cmd/subtitler`.
5. Run `"$CAPTURE" launch version "$WORK/subtitler" version`. Ready means it prints a version and exits 0.
6. Confirm `ffmpeg`, `ffprobe`, and `jq` resolve on `PATH`.
7. Run `.cursor/skills/verify-subtitler/scripts/tone-mkv.sh`. The script writes `$WORK/tone.mkv`.
8. Write the two config files.

```bash
cat >"$WORK/config.yaml" <<'YAML'
dry_run: true
telemetry:
  enabled: false
YAML
cat >"$WORK/generate-config.yaml" <<YAML
dry_run: false
state_path: "$WORK/generate-state.json"
telemetry:
  enabled: false
YAML
```

`config.yaml` is the dry-run config for doctor, inspect, scan, and status. `generate-config.yaml` is only for generate. It turns the dry run off so generate reaches the API-key check, and it gives generate its own state file.

Do not copy `config.example.yaml`. Do not set a Sonarr URL or a Radarr URL.

Run every later command with the current directory set to `$WORK`. `capture.sh` does this. The default state path is `subtitler-state.json` in the current directory. The repo root must not gain that file.

## Doctor

Run doctor after Launch, and again before a drive when a command has failed. Follow [doctor.md](features/doctor.md).

The instance is worth driving when doctor exits 0 and stdout contains `media tools available`. The empty-key warning is expected. It means generate cannot finish a subtitle. Doctor needs `$WORK/config.yaml` to exist. Also require the Launch `version` capture to have exited 0.

## Drive

Deliver each recipe command through `control-cli` from `cursor-team-kit` when it is available. `control-cli` is not in this repo. When it is absent, run the same command directly in a shell. Record that choice as `delivery` in `$EVIDENCE/run.txt`. Either way, judge the run by the command's exit code and stdout.

The offline command that finishes is the first step of [inspect.md](features/inspect.md). Run it once. That capture serves both this check and the inspect recipe.

```bash
"$CAPTURE" inspect inspect-audio "$WORK/subtitler" inspect -config "$WORK/config.yaml" "$WORK/tone.mkv"
```

Pass when the exit code is 0 and stdout contains `audio stream` and `codec=aac`.

Errors from this CLI are slog lines on stdout. Read-only diagnostics are the exit code, stdout, stderr, and `find` of `$WORK`.

## Evidence

Wrap every recipe command in `"$CAPTURE" FEATURE SUB_FEATURE COMMAND...`. `capture.sh` runs the command from `$WORK`, prints its output, exits with its exit code, and writes these files.

- `$EVIDENCE/FEATURE/SUB_FEATURE.cmd` is the command.
- `$EVIDENCE/FEATURE/SUB_FEATURE.rc` is the exit code.
- `$EVIDENCE/FEATURE/SUB_FEATURE.stdout` and `.stderr` are the output.
- `$EVIDENCE/FEATURE/SUB_FEATURE.find` is the sorted `find` listing of `$WORK` after the command.

`FEATURE` is the feature file name without `.md`, or `launch` for Launch. `capture.sh` refuses to overwrite an artifact, so one `EVIDENCE` holds exactly one run. `$EVIDENCE/run.txt` names the commit and the work directory for that run.

Drive the real binary. Do not call a library function or a test fake in its place. For generate, also capture the state entry and the `.srt` check. With no API key, a passing generate proof is exit code 1, the API-key error on stdout, a state entry with `attempts` 1, and no `.srt`.

## Cleanup

The CLI exits on its own. Do not kill by process name.

Delete these paths when they exist, then remove `$WORK`.

- `$WORK/tone.mkv`
- `$WORK/config.yaml`
- `$WORK/generate-config.yaml`
- `$WORK/subtitler`
- `$WORK/subtitler-state.json`
- `$WORK/generate-state.json`

```bash
rm -f "$WORK/tone.mkv" "$WORK/config.yaml" "$WORK/generate-config.yaml" \
  "$WORK/subtitler" "$WORK/subtitler-state.json" "$WORK/generate-state.json"
rmdir "$WORK"
```

When `rmdir` fails, a command wrote a path this skill does not expect. Record the leftover paths as a finding before deleting them.

Leave `$EVIDENCE` in place. Cleanup passes when `$WORK` is gone and `find "$EVIDENCE" -type f` still lists every artifact. A cleanup that deletes evidence fails the proof.

## Helpers

Both scripts are executable.

- `scripts/tone-mkv.sh` runs ffmpeg and writes `$WORK/tone.mkv`. Run it from the repo root with `WORK` set to the empty work directory. `ffmpeg` must resolve on `PATH`.
- `scripts/capture.sh` runs one command and saves its evidence. It needs `WORK` and `EVIDENCE` set, and `EVIDENCE` outside `WORK`. It exits 64 when it is misused, for example when an artifact already exists.

## Keep the map current

This skill states no cadence. When a change touches a CLI command, flag, log message, config default, or state field that a recipe names, update the matching feature file and `features/README.md` in the same change. Then run the affected recipes from a new Launch and check the captured evidence.
