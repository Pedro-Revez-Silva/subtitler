# subtitler

Headless subtitle generator and library scanner for Sonarr/Radarr libraries.

The service periodically asks Sonarr and Radarr where media files live, removes or quarantines external subtitle clutter according to policy, sends extracted audio chunks to OpenAI for transcription, and writes Jellyfin-compatible `.srt` sidecars next to the media.

## Current status

This is an early working implementation:

- Sonarr/Radarr API discovery
- path mapping for container path differences
- external subtitle cleanup: `keep`, `quarantine`, or `delete`
- generation strategies: `missing_only`, `generated_only`, `force`
- MKV/MP4 audio stream inspection with `ffprobe`
- audio-first subtitle timing from a preferred source audio track
- optional embedded subtitle extraction when explicitly enabled
- audio extraction/chunking with `ffmpeg`
- OpenAI transcription to SRT
- optional SRT translation while preserving timestamps
- JSON state file for generated outputs and failures
- periodic daemon scans that process only missing subtitle jobs
- per-scan job limit to control first-run cost
- Sentry telemetry for a one-time anonymous installation signal

Embedded subtitle removal is intentionally not implemented because it requires remuxing media files. Extraction of embedded subtitle tracks is supported, but audio-first generation is the default because it ties subtitle timings to the audio track being watched.

## Server setup with Docker

The recommended setup is Docker. The image includes `ffmpeg` and `ffprobe`, so the host only needs Docker and access to the media folders.

### 1. Copy the example files

```bash
cp config.example.yaml config.yaml
cp .env.example .env
```

Edit `.env`:

```env
OPENAI_API_KEY=your-openai-api-key
SONARR_API_KEY=your-sonarr-api-key
RADARR_API_KEY=your-radarr-api-key
PUID=1000
PGID=1000
```

Set `PUID` and `PGID` to the user that owns your media files:

```bash
id -u
id -g
```

### 2. Configure Sonarr and Radarr

Set the ARR URLs and API keys in `config.yaml`:

```yaml
sonarr:
  url: http://sonarr:8989
  api_key: ${SONARR_API_KEY}

radarr:
  url: http://radarr:7878
  api_key: ${RADARR_API_KEY}
```

Use URLs that are reachable from the subtitler container. If subtitler runs in the same Docker network as Sonarr/Radarr, container names like `http://sonarr:8989` can work. If Sonarr/Radarr run on the host or another machine, use the reachable host/IP instead.

### 3. Mount the media paths

Subtitler does not need its own media-folder config. It asks Sonarr and Radarr for media file paths through their APIs.

The container must be able to read and write those exact paths. If Radarr reports:

```bash
/media/movies/Movie Name/Movie.mkv
```

then `docker-compose.example.yaml` should mount the same host path into the same container path:

```yaml
volumes:
  - ./config.yaml:/config/config.yaml:ro
  - ./data:/config
  - /media:/media
```

If matching paths are impossible, use `path_mappings` in `config.yaml`:

```yaml
subtitles:
  path_mappings:
    - from: /movies
      to: /media/movies
```

That means a Radarr path like `/movies/Movie Name/Movie.mkv` is processed as `/media/movies/Movie Name/Movie.mkv` inside the subtitler container.

### 4. Keep first-run settings safe

The example config starts in dry-run mode:

```yaml
dry_run: true
```

It also defaults to conservative subtitle behavior:

```yaml
subtitles:
  source_audio_languages:
    - en
    - auto
  source_subtitle_language: en
  strategy: missing_only
  cleanup:
    external_subtitles: keep
processing:
  max_jobs_per_scan: 1
```

This means the first real daemon run will only add missing subtitles, will not remove existing sidecars, and will process at most one media item per scan.

### 5. Build and validate

```bash
docker compose --env-file .env -f docker-compose.example.yaml build
docker compose --env-file .env -f docker-compose.example.yaml run --rm subtitler doctor -config /config/config.yaml
```

### 6. Run a dry scan

Keep `dry_run: true` and run. If you have already changed the config, add `-dry-run` to force a preview:

```bash
docker compose --env-file .env -f docker-compose.example.yaml run --rm subtitler scan -config /config/config.yaml
```

Check the logs. The scan should find media through Sonarr/Radarr and report what subtitles it would generate.

### 7. Run one real job

Set `dry_run: false` in `config.yaml` and keep `max_jobs_per_scan: 1`.

Run one scan manually:

```bash
docker compose --env-file .env -f docker-compose.example.yaml run --rm subtitler scan -config /config/config.yaml
```

Expected result: one missing media item gets `.subtitler.en.srt` and/or `.subtitler.pt.srt` sidecars next to the video file.

### 8. Start the daemon

Once the dry scan and one real scan look right:

```bash
docker compose --env-file .env -f docker-compose.example.yaml up -d
docker compose --env-file .env -f docker-compose.example.yaml logs -f subtitler
```

The daemon repeats the library scan every `processing.scan_interval`.

## Telemetry

Telemetry is enabled by default and sends one anonymous `subtitler.installed` message to Sentry the first time an installation starts. The marker is stored in the state file, so normal restarts do not send another install event.

This is intended to answer basic maintainer questions such as installation count and most-used version. To disable it:

```yaml
telemetry:
  enabled: false
```

You can also set `SUBTITLER_TELEMETRY=off`. `SENTRY_DSN` or `telemetry.sentry_dsn` can be set to override the built-in Sentry project DSN for private builds.

Telemetry sends:

- a random installation ID generated locally and stored in the state file
- app version, operating system, architecture, and command mode

Telemetry does not send media paths, titles, subtitle text, OpenAI prompts, API keys, ARR URLs, or hostnames. Sentry error/panic capture is not enabled by default because arbitrary errors can contain file paths.

## Local development

You need Go 1.25.11 or newer plus `ffmpeg`/`ffprobe` available on the host for local non-Docker runs.

```bash
export OPENAI_API_KEY=...
export SONARR_API_KEY=...
export RADARR_API_KEY=...
go run ./cmd/subtitler doctor -config config.yaml
go run ./cmd/subtitler scan -config config.yaml -dry-run
```

To process one file manually:

```bash
go run ./cmd/subtitler generate -config config.yaml "/path/to/movie.mkv"
```

## Standalone file mode

Subtitler can also run as a plain executable without Sonarr/Radarr and without a config file. This is useful when you just want subtitles for one file.

```bash
export OPENAI_API_KEY=...
go build -o subtitler ./cmd/subtitler
./subtitler generate -langs en,pt-PT "/path/to/movie.mkv"
```

If `config.yaml` is not present and `-config` is not passed, `generate` uses safe defaults:

- reads `OPENAI_API_KEY` from the environment
- writes `.subtitler.<language>.srt` next to the video
- uses `whisper-1` for timestamped SRT output
- uses `gpt-4o-mini` only when translation is needed
- keeps local state in `subtitler-state.json`

Passing `-config custom.yaml` makes the config explicit; if that file is missing, the command fails instead of silently falling back.

To run continuously:

```bash
go run ./cmd/subtitler daemon -config config.yaml
```

The static project website lives under `docs/` and can be served by GitHub Pages or any static file host.

Brand assets live under `assets/`. The website keeps its own SVG copies in `docs/` so GitHub Pages can serve the favicon and navigation logo directly.

## Release cadence

CI runs on pull requests and pushes to `main`/`dev`. It runs the local quality gate, `go vet`, a binary build, a Docker build check, and an advisory `govulncheck` scan.

The local quality gate is:

```bash
# Fast local check
bin/quality fast

# Pre-commit style check: format, tests, and 95% core coverage
bin/quality commit

# Pre-push style check: commit gate, vet, Docker build, and govulncheck
bin/quality push

# Coverage only
bin/quality coverage

# Whole-repo coverage report, currently informational
bin/quality coverage-all
```

The enforced coverage gate is 95% for deterministic core packages: `internal/config` and `internal/subtitle`. Whole-repo coverage is reported separately because the app orchestration, media shell wrappers, OpenAI HTTP boundary, and telemetry contain integration-heavy behavior that should move behind targeted fakes or integration tests before being made part of a hard public gate.

Public releases are scheduled instead of created on every merge. The release workflow runs every 12 hours and can also be started manually. It creates a release only when `main` has changed since the latest release tag.

Release tags use date-based versions:

```text
vYYYY.MM.DD.N
```

For example, the first release on June 5, 2026 is `v2026.06.05.1`; the second release that same day is `v2026.06.05.2`.

Each release publishes Docker images to GitHub Container Registry with these tags:

```text
ghcr.io/Pedro-Revez-Silva/subtitler:latest
ghcr.io/Pedro-Revez-Silva/subtitler:vYYYY.MM.DD.N
```

## Gradual library scanning

To let the scanner process the library gradually, keep:

```yaml
dry_run: false
subtitles:
  strategy: missing_only
  cleanup:
    external_subtitles: keep
processing:
  scan_interval: 30m
  max_jobs_per_scan: 1
```

`max_jobs_per_scan` limits media items that actually need subtitle work. Files that already have the required sidecars do not consume that budget. Set it to `0` only when you are comfortable letting the scanner process every missing item it finds in one scan.

## Important config choices

```yaml
subtitles:
  required_languages: [en, pt-PT]
  source_audio_languages: [en, auto]
  source_subtitle_language: en
  strategy: missing_only
  embedded:
    action: ignore
  cleanup:
    external_subtitles: keep
```

`source_audio_languages` controls which audio track is used to create the timed source transcript. `source_subtitle_language` controls the intermediate subtitle language written from that audio before translating to the other required languages. With the example above, Subtitler prefers English audio, writes English timed cues, then translates those same cues to Portuguese.

`missing_only` means the service writes required languages only when it cannot find matching sidecars. Embedded subtitle extraction is available only when `embedded.action: extract` is enabled; keep it disabled for audio-first timing.

If you want generated subtitles to replace subtitle clutter, use `generated_only` with `quarantine` first. Once you trust the file matching behavior, switch to:

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
