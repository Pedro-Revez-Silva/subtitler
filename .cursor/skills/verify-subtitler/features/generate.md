# Generate a subtitle

Generate writes one sidecar subtitle for one video. With no API key it stops before transcription and writes no `.srt`.

## Sub-features

- `generate-no-key` fails when `OPENAI_API_KEY` is unset.
- `generate-state` records that failure in the generate state file.

## How to get to it (user POV)

- Run `subtitler generate -langs en /path/to/video.mkv`.

## Driving it with control-cli

Preconditions:

- `$WORK/tone.mkv` exists.
- `$WORK/generate-config.yaml` exists as Launch wrote it, with `dry_run: false`, `state_path` set to `$WORK/generate-state.json`, and `telemetry.enabled: false`.
- `$WORK/generate-state.json` does not exist. Check with `test ! -e "$WORK/generate-state.json"`. When it exists, this run has already driven generate. Start a new run from Launch.
- `OPENAI_API_KEY` is unset.
- `SUBTITLER_TELEMETRY` is `off`.

- **Generate with no key.** Deliver `"$CAPTURE" generate generate-no-key env -u OPENAI_API_KEY SUBTITLER_TELEMETRY=off "$WORK/subtitler" generate -config "$WORK/generate-config.yaml" -langs en "$WORK/tone.mkv"`. The exit code is 1. Stdout contains `level=ERROR msg="generate failed" error="openai.api_key is required to generate subtitles"`. Stdout does not contain `dry run would generate subtitles`.
- **Read the state file.** Deliver `"$CAPTURE" generate generate-state jq -e --arg path "$WORK/tone.mkv" '.files[$path] | select(.attempts == 1 and .last_error == "openai.api_key is required to generate subtitles" and .languages == {})' "$WORK/generate-state.json"`. The exit code is 0, and stdout is the state entry for the tone file. Any other exit code means the entry does not match.
- **No sidecar.** Read `$EVIDENCE/generate/generate-no-key.find`. It lists `$WORK/generate-state.json` and no `.srt`.
- **Proof.** `capture.sh` saved `$EVIDENCE/generate/generate-no-key.*` and `$EVIDENCE/generate/generate-state.*`.

## Gotchas

- Always pass `-config "$WORK/generate-config.yaml"`. Without `-config`, generate loads `$WORK/config.yaml`, which sets `dry_run: true`. It then logs `dry run would generate subtitles`, exits 0, and records `attempts` 0.
- Generate falls back to defaults only when `-config` was not passed and `config.yaml` is absent from the current directory. Launch writes that file, so the fallback does not apply here. Inspect does not fall back.
- Each rerun against the same state file adds 1 to `attempts`. After `processing.max_attempts` (3 by default), generate logs `skipping failed media until retry delay passes` and exits 0. A repeat proof needs a new Launch.
- Leave `OPENAI_API_KEY` unset. Do not download media. Do not replace this command with a test fake.
- A later run that has a key must check the sidecar bytes and the `languages` entry. This recipe stops at the no-key failure.
- There is no retime command on this CLI. Do not treat `subtitle.Offset` or the owner sync path as driven until a real command runs them.
