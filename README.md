# subtitler

Headless subtitle generator for Sonarr/Radarr libraries.

The service periodically asks Sonarr and Radarr where media files live, removes or quarantines external subtitle clutter according to policy, sends extracted audio chunks to OpenAI for transcription, and writes Jellyfin-compatible `.srt` sidecars next to the media.

## Current status

This is an early working implementation:

- Sonarr/Radarr API discovery
- path mapping for container path differences
- external subtitle cleanup: `keep`, `quarantine`, or `delete`
- generation strategies: `missing_only`, `generated_only`, `force`
- MKV/MP4 audio stream inspection with `ffprobe`
- embedded subtitle extraction before transcription when matching language tracks exist
- audio extraction/chunking with `ffmpeg`
- OpenAI transcription to SRT
- optional SRT translation while preserving timestamps
- JSON state file for generated outputs and failures

Embedded subtitle removal is intentionally not implemented because it requires remuxing media files. Extraction of embedded subtitle tracks is supported and is preferred before transcription.

## Quick start

You need `ffmpeg`/`ffprobe` available on the host, or use the Docker image where they are installed for you.

```bash
cp config.example.yaml config.yaml
cp .env.example .env
export OPENAI_API_KEY=...
export SONARR_API_KEY=...
export RADARR_API_KEY=...
go run ./cmd/subtitler scan -config config.yaml
```

`config.example.yaml` starts with `dry_run: true`. The scan will show what it would do without deleting or generating files.

To process one file manually:

```bash
go run ./cmd/subtitler generate -config config.yaml "/path/to/movie.mkv"
```

To run continuously:

```bash
go run ./cmd/subtitler daemon -config config.yaml
```

To validate the runtime:

```bash
go run ./cmd/subtitler doctor -config config.yaml
```

## Test plan

Start with Docker so `ffmpeg` and `ffprobe` are contained in the service image:

```bash
cp config.example.yaml config.yaml
cp .env.example .env
```

Edit `.env` with real keys. Do not paste API keys into chat or commit them.

Set `PUID`/`PGID` to the user that owns your media files so generated sidecars are not written as root:

```bash
id -u
id -g
```

Build the image and check the runtime:

```bash
docker compose --env-file .env -f docker-compose.example.yaml build
docker compose --env-file .env -f docker-compose.example.yaml run --rm subtitler doctor -config /config/config.yaml
```

The first safe ARR test is a dry run. Keep `dry_run: true` in `config.yaml`:

```bash
docker compose --env-file .env -f docker-compose.example.yaml run --rm subtitler scan -config /config/config.yaml
```

The first real transcription test should use one short media file and `dry_run: false`:

```bash
docker compose --env-file .env -f docker-compose.example.yaml run --rm subtitler generate -config /config/config.yaml "/media/path/to/test-file.mkv"
```

Expected result:

```text
test-file.en.srt
test-file.pt-PT.srt
```

next to the media file, plus state recorded under `/config/subtitler-state.json`.

## Important config choices

```yaml
subtitles:
  required_languages: [en, pt-PT]
  strategy: generated_only
  cleanup:
    external_subtitles: quarantine
```

`generated_only` means the service wants the final external sidecars to be the generated subtitles for the required languages.

Use `quarantine` first. Once you trust the file matching behavior, switch to:

```yaml
cleanup:
  external_subtitles: delete
```

## Model note

Timestamped subtitle generation currently uses OpenAI `whisper-1` with `response_format=srt`. Newer transcription models can produce stronger transcripts, but the current implementation requires native SRT timestamps for usable subtitle timing.

## Packaging note

The intended setup-and-forget deployment is Docker. The image installs `ffmpeg` and `ffprobe`, so the media tooling is contained in the service container. If you run the Go binary directly on a host, those tools must be installed or configured with:

```yaml
tools:
  ffmpeg: /path/to/ffmpeg
  ffprobe: /path/to/ffprobe
```
