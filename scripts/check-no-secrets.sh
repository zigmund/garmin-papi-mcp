#!/usr/bin/env bash
# Fails if tracked files include credentials or local agent files.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

bad="$(git ls-files | grep -E '^(test/api\.key|test/access\.token|CLAUDE\.md|AGENTS\.md|docs/superpowers/|\.claude/|\.beads/|\.superpowers/)' || true)"
if [[ -n "$bad" ]]; then
  echo "error: forbidden files are tracked:" >&2
  echo "$bad" >&2
  exit 1
fi
echo "ok: no forbidden files tracked"
