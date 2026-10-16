#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 3 ]]; then
  echo "Usage: capture.sh FEATURE SUB_FEATURE COMMAND [ARG...]" >&2
  exit 64
fi

feature="$1"
sub_feature="$2"
shift 2

for name in "$feature" "$sub_feature"; do
  if [[ ! "$name" =~ ^[a-z][a-z0-9-]*$ ]]; then
    echo "Name '$name' must use lowercase letters, digits, and hyphens." >&2
    exit 64
  fi
done

for var in WORK EVIDENCE; do
  if [[ -z "${!var:-}" ]]; then
    echo "$var is unset. Run Launch first." >&2
    exit 64
  fi
  if [[ ! -d "${!var}" ]]; then
    echo "$var is not a directory." >&2
    exit 64
  fi
done

work="$(cd "$WORK" && pwd -P)"
evidence="$(cd "$EVIDENCE" && pwd -P)"
case "$evidence/" in
  "$work/"*)
    echo "EVIDENCE must be outside WORK." >&2
    exit 64
    ;;
esac

out="$evidence/$feature/$sub_feature"
if compgen -G "$out.*" >/dev/null; then
  echo "$out already has artifacts. Start a new run with a new WORK and EVIDENCE." >&2
  exit 64
fi
mkdir -p "$evidence/$feature"

printf '%q ' "$@" | sed 's/ $//' >"$out.cmd"
cd "$WORK"
rc=0
"$@" >"$out.stdout" 2>"$out.stderr" || rc=$?
echo "$rc" >"$out.rc"
find "$WORK" -print | LC_ALL=C sort >"$out.find"

cat "$out.stdout"
cat "$out.stderr" >&2
exit "$rc"
