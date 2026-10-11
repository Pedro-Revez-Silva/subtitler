# Subtitle ownership

Enable `management.enabled` to replace the legacy generation-only scanner.
The daemon reconciles Radarr/Sonarr inventory, detects media imports and deleted
or edited captions, and maintains a SQLite catalogue outside the library.
Only `en` and `pt-PT` are supported by this ownership policy. Generic `pt` is
ambiguous, and `pt-BR` cannot satisfy `pt-PT`.

For each language, processing follows this order:

1. Inspect existing sidecars and extract embedded text subtitles. Forced tracks
   do not satisfy the requirement for a full subtitle. PGS captions are read
   locally with OCR before searching providers.
2. Validate the text/timestamps and try local FFsubsync repair against the selected
   dialogue audio. Repair handles offsets and frame-rate differences. Structural
   and sampled word checks run again after repair. Strong matching words can
   establish offsets and drift; already aligned subtitles keep their timestamps.
3. If no usable local subtitle remains, search the configured providers and check
   every downloaded candidate through the same pipeline. Exact movie identity or
   series/season/episode matching is required; OpenSubtitles requests `pt-pt`.
4. Only after searches for both languages finish, consider generation. Reuse an
   accepted subtitle for translation before transcribing the audio again. Local
   Whisper can generate a missing track from confirmed English/PT-PT audio. Paid
   transcription is the final fallback, enabled explicitly with a daily limit.
   Original foreign audio can be translated locally into English before PT-PT
   translation. Text translations inherit the accepted source's exact timestamps.
5. Publish atomically, save provenance and verification results, and quarantine
   superseded/rejected/unwanted captions. Provider outages defer generation.

FFsubsync uses speech/silence patterns and **does not prove that words match**.
For same-language audio, `verify_words` compares text and word timestamps in three
60-second samples using local Whisper base/int8. Mismatches reject a candidate;
inconclusive checks remain queued without triggering paid generation. Untried
provider candidates also defer generation. These are sampled checks, not a proof of every
line. Translated subtitles cannot be verified by literal word comparison against
foreign audio: their catalogue explicitly records `translated_text_unverified`.
Their identity is established by an embedded stream or provider metadata, or by
translation from an accepted source. Existing translated sidecars of unknown
provenance require a provider replacement. Blu-ray PGS bitmap captions use local
Tesseract OCR (English/Portuguese), retaining their original timestamps. OCR
confidence and the ordinary text/timing checks must pass. Unsupported bitmap
formats and cropped PGS compositions remain for review.

Create a private JSON provider file (`chmod 600`) outside the repository:

```json
{
  "providers": ["opensubtitlescom", "gestdown", "yifysubtitles"],
  "credentials": {
    "opensubtitlescom": {"username": "your-account", "password": "your-password"}
  }
}
```

Subliminal's additional supported providers can be configured in the same file.
YIFY compatibility searches are English-only because its generic Portuguese
category does not establish the region. Free-provider quotas and unavailable
providers are respected; an outage never causes paid generation.

```sh
subtitler status -config config.yaml
subtitler report -config config.yaml -lang pt-PT -reason out_of_sync /media/Film.mkv
# A unique catalogue title can be used instead of the media path.
subtitler report -config config.yaml -lang pt-PT -reason out_of_sync "Film"
```

Feedback reasons are `out_of_sync`, `wrong_text`, `wrong_language`, and `missing`.
Reports include the current media fingerprint and caption hash. The next scan
quarantines the reported track, rejects its source and both input/output hashes,
and searches for an alternative. Translations derived from a bad source are
invalidated too. A stale report cannot reject a newly imported movie.

`prune_embedded` and `prune_audio` perform a copy remux of MKV files, retaining
video, metadata, chapters and attachments. Desired full embedded subtitles are
removed only once both canonical sidecars exist. Known unwanted languages can
be removed earlier; desired forced tracks are retained. Audio keeps English,
PT-PT and the original language from ARR. Unknown audio metadata and ambiguous
Portuguese audio remain for review, rather than risking the sole original/dub.
Ambiguous subtitle tracks are quarantined only after both canonical replacements
exist; until then they remain available for review.
Other containers remain for review. A verified backup hardlink is made outside
the library before replacing its media path. Seeding hardlinks keep their original
bytes. This consumes additional storage for remuxed media; the free-space floor
is enforced. MergerFS backups use the source's physical branch when pool link
policies reject a cross-directory link; mount each branch at its host path in
Docker. The pool's mount settings are untouched. Backups are not automatically deleted.

Paid mode uses only `whisper-1` and `gpt-4o-mini` through the official API. Model
prices were checked on 2026-10-11. Every request reserves a conservative upper
bound in SQLite **before** making the call; crashes, failures and restarts cannot
reset the daily UTC allowance. Translation batches preserve cue timestamps and
are cached for resumption. Audio is chunked to stay below upload limits. Unknown
models/endpoints fail closed. This limit applies to this manager, not other apps
sharing the API key. No credentials are written to the catalogue or logs.

Native development:

```sh
python3 -m venv /disk/cache/subtitler-venv
/disk/cache/subtitler-venv/bin/pip install -r manager/requirements.txt
SUBTITLER_PYTHON=/disk/cache/subtitler-venv/bin/python bin/quality push
```

Set `management.python` and `management.script` for native execution. In Docker
the defaults are `python3` and `/opt/subtitler/manager.py`. Back up the ownership
SQLite database (including WAL if it is live), provider file and quarantine,
alongside the existing configuration. Keep Bazarr stopped once the manager takes
over, so two services cannot race to replace the same sidecars.
