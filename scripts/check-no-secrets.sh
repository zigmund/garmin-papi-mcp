#!/usr/bin/env bash
# Fails if tracked files include credentials or local agent files, at the repo
# root or nested in any directory.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

bad="$(git ls-files | grep -E '(^|/)(CLAUDE\.md|AGENTS\.md)$|(^|/)(\.claude|\.agents|\.codex|\.beads|memory|\.superpowers)/|^(test/api\.key|test/access\.token)$|(^|/)test/(api\.key|access\.token)$|(^|/)docs/superpowers/' || true)"
if [[ -n "$bad" ]]; then
  echo "error: forbidden files are tracked:" >&2
  echo "$bad" >&2
  exit 1
fi
echo "ok: no forbidden files tracked"
