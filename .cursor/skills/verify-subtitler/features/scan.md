# Scan the library

Scan lists planned subtitle work for the Sonarr library and the Radarr library.

## Sub-features

- `scan-dry-run` lists planned work and does not process files.

## How to get to it (user POV)

- Run `subtitler scan -config config.yaml -dry-run`.

## Driving it with control-cli

Preconditions:

- `$WORK/config.yaml` sets a Sonarr URL or a Radarr URL.
- The verification report did not meet this precondition, and it did not run this command.

- **Dry-run scan.** Deliver `"$WORK/subtitler" scan -config "$WORK/config.yaml" -dry-run` through `control-cli`. The boot config has no Sonarr URL and no Radarr URL, so this journey fails. Save the failure. Do not replace the command with a different one.
- **Proof.** Save the command, the exit code, and stdout under `/tmp/verify-subtitler-evidence`. Record the unmet precondition beside them. Do not record the journey as passed.

## Gotchas

- The verification report did not run `scan -dry-run`. The missing Sonarr URL and Radarr URL are a failed journey, not a skipped assertion.
- Do not add an ARR URL to make this journey pass.
- Scan rejects a command that passes both `-process` and `-dry-run`.
