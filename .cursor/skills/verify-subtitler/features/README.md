# Subtitler verification map

This directory is the maintained source for verifying the Subtitler CLI. Read this index, then follow the matching feature file.

## Baseline preconditions

- `WORK` is a new empty directory on disk, outside the repo root. The current directory for each command is `$WORK`.
- `EVIDENCE` is a new empty directory on disk, outside `$WORK` and outside the repo root. `CAPTURE` is the absolute path of `scripts/capture.sh`.
- Launch has built `$WORK/subtitler`, written `$WORK/tone.mkv`, written `$WORK/config.yaml` and `$WORK/generate-config.yaml`, and written `$EVIDENCE/run.txt`.
- `GOTOOLCHAIN` is `go1.26.9`. `SUBTITLER_TELEMETRY` is `off`. `OPENAI_API_KEY` is unset.
- `ffmpeg`, `ffprobe`, and `jq` resolve on `PATH`.
- Doctor has exited 0, and stdout contains `media tools available`.
- Deliver each command through `control-cli` when it is available. When it is absent, run the same command directly in a shell. Record which one in `$EVIDENCE/run.txt`. Do not drive a work directory this run did not create.

## Driving conventions

- Start every recipe from the baseline state unless its preconditions say otherwise.
- Treat every command as literal. Keep the paths and flags unchanged.
- Errors from the CLI are slog lines on stdout.
- Read-only diagnostics are the exit code, stdout, stderr, and `find` of `$WORK`.
- Cleanup deletes the files in `$WORK`, then `$WORK` itself. It must leave `$EVIDENCE` in place.

## Proof and failure reporting

- Wrap each recipe command in `"$CAPTURE" FEATURE SUB_FEATURE`. It saves `$EVIDENCE/FEATURE/SUB_FEATURE.{cmd,rc,stdout,stderr,find}`.
- `FEATURE` is the feature file name without `.md`. That directory name records the feature file for every artifact.
- Capture the command and the files it changed, not only the exit code. The `.find` listing shows the files.
- Report an unreachable command with the command you ran and the unmet precondition.
- A missing precondition is a failed journey. Do not report that journey as skipped, and do not report it as passed through a different command.
- A recipe that expects a failure names its exit code and message. A different exit code or message is a broken run, not the expected gap. Re-run doctor, and report the failure you saw.

## Feature entry contract

Each feature file starts with an H1 and one paragraph. It then uses these four H2 headings, in order.

1. `Sub-features`
2. `How to get to it (user POV)`
3. `Driving it with control-cli`
4. `Gotchas`

## Features

- [Inspect a file](./inspect.md) prints audio streams and sidecar subtitles for one video.
- [Run doctor](./doctor.md) checks ffmpeg, ffprobe, the API key, and ARR URLs.
- [Generate a subtitle](./generate.md) records the no-key failure and the state file it writes.
- [Scan the library](./scan.md) is the dry-run scan. The boot config cannot meet its precondition.
- [Read status](./status.md) is the owner status command. The boot config cannot meet its precondition.
