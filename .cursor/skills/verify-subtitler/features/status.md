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
- Launch does not install those packages. The verification report did not run `status`.

- **Read status.** Deliver `"$WORK/subtitler" status -config "$WORK/config.yaml"` through `control-cli`. The boot config leaves management off, and Launch does not install the Python owner, so this journey fails. Save the failure. Do not replace the command with a different one.
- **Proof.** Save the command, the exit code, and stdout under `/tmp/verify-subtitler-evidence`. Record the unmet precondition beside them. Do not record the journey as passed.

## Gotchas

- The verification report did not run `status`. The boot config leaves management off. Launch does not install the Python owner. That gap is a failed journey, not a skipped assertion.
- Do not set `management.enabled` to make this journey pass. Do not `pip install` `manager/requirements.txt` to make this journey pass.
- Status takes no file arguments. The CLI rejects a path on this command.
