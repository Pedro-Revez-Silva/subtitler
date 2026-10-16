# Read status

Status asks the ownership engine for the catalogue when management is on.

## Sub-features

- `status-read` runs `subtitler status` against the ownership engine.

## How to get to it (user POV)

- Run `subtitler status -config config.yaml`.

## Driving it with control-cli

Preconditions:

- `management.enabled` is true in the config under test.
- The Python owner from `manager/requirements.txt` is installed. The default script path is `/opt/subtitler/manager.py` unless config sets `management.script`.
- Launch sets neither, so these preconditions are unmet.

- **Read status.** Deliver `"$CAPTURE" status status-read "$WORK/subtitler" status -config "$WORK/config.yaml"`. The journey fails. Do not replace the command with a different one.
- **Expected failure.** The exit code is 1. Stdout contains `level=ERROR msg="management command failed" error="management is not enabled"`. Only that exit code and line show the unmet precondition. Any other result is a broken run. Re-run doctor and report what you saw.
- **Proof.** `capture.sh` saved `$EVIDENCE/status/status-read.*`. Write the unmet precondition to `$EVIDENCE/status/status-read.unmet`. Record the journey as failed, not passed.

## Gotchas

- The boot config leaves management off. Launch does not install the Python owner. That gap is a failed journey, not a skipped assertion.
- Do not set `management.enabled` to make this journey pass. Do not `pip install` `manager/requirements.txt` to make this journey pass.
- Status takes no file arguments. The CLI rejects a path on this command.
