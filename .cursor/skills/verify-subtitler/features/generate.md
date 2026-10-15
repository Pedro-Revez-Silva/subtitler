# Generate a subtitle

Generate writes one sidecar subtitle for one video. With no API key it stops before transcription and writes no `.srt`.

## Sub-features

- `generate-no-key` fails when `OPENAI_API_KEY` is unset.
- `generate-state` records that failure in `subtitler-state.json`.

## How to get to it (user POV)

- Run `subtitler generate -langs en /path/to/video.mkv`.

## Driving it with control-cli

Preconditions:

- The current directory is `$WORK`.
- `$WORK/tone.mkv` exists.
- `OPENAI_API_KEY` is unset.
- `SUBTITLER_TELEMETRY` is `off`.

- **Generate with no key.** Deliver `env -u OPENAI_API_KEY SUBTITLER_TELEMETRY=off "$WORK/subtitler" generate -langs en "$WORK/tone.mkv"` through `control-cli`. The exit code is 1. Stdout contains `openai.api_key is required to generate subtitles`. Generate writes no `.srt` beside the tone file.
- **Read the state file.** Open `$WORK/subtitler-state.json`. The file entry has `attempts` set to 1, `last_error` equal to that API-key message, and `languages` empty.
- **Proof.** Save the command, the exit code, stdout, stderr, and `subtitler-state.json` under `/tmp/verify-subtitler-evidence`. Run `find "$WORK" -name '*.srt'`. That listing is empty.

## Gotchas

- Leave `OPENAI_API_KEY` unset. Do not download media. Do not replace this command with a test fake.
- A later run that has a key must check the sidecar bytes and the `languages` entry. This recipe stops at the no-key failure.
- Generate falls back to defaults when `config.yaml` is absent and `-config` was not passed. Inspect does not fall back.
- There is no retime command on this CLI. Do not treat `subtitle.Offset` or the owner sync path as driven until a real command runs them.
- The CLI writes the state file in the current directory when `state_path` is relative. Run from `$WORK`, not from the repo root.
