# Scan the library

Scan lists planned subtitle work for the Sonarr library and the Radarr library.

## Sub-features

- `scan-dry-run` lists planned work and does not process files.

## How to get to it (user POV)

- Run `subtitler scan -config config.yaml -dry-run`.

## Driving it with control-cli

Preconditions:

- `$WORK/config.yaml` sets a Sonarr URL or a Radarr URL.
- The Launch config sets neither, so this precondition is unmet.

- **Dry-run scan.** Deliver `"$CAPTURE" scan scan-dry-run "$WORK/subtitler" scan -config "$WORK/config.yaml" -dry-run`. The journey fails. Do not replace the command with a different one.
- **Expected failure.** The exit code is 1. Stdout contains `level=ERROR msg="scan failed" error="at least one of radarr.url or sonarr.url is required for scanning"`. Only that exit code and line show the unmet precondition. Any other result is a broken run. Re-run doctor and report what you saw.
- **Proof.** `capture.sh` saved `$EVIDENCE/scan/scan-dry-run.*`. Write the unmet precondition to `$EVIDENCE/scan/scan-dry-run.unmet`. Record the journey as failed, not passed.

## Gotchas

- The missing Sonarr URL and Radarr URL are a failed journey, not a skipped assertion.
- Do not add an ARR URL to make this journey pass.
- Scan rejects a command that passes both `-process` and `-dry-run`.
