#!/usr/bin/env bash
# Tests scripts/check-no-secrets.sh in scratch repos under mktemp; the real
# repository is never touched.
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/check-no-secrets.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail=0
n=0

new_repo() {
  n=$((n + 1))
  repo="$tmp/repo$n"
  mkdir -p "$repo/scripts"
  git -C "$repo" init -q
  git -C "$repo" config user.email test@example.invalid
  git -C "$repo" config user.name test
  cp "$script" "$repo/scripts/check-no-secrets.sh"
  echo hello > "$repo/README.md"
  git -C "$repo" add README.md scripts
}

check_clean() {
  new_repo
  if out="$(cd "$repo" && bash scripts/check-no-secrets.sh 2>&1)"; then
    echo "ok   clean tree exits 0"
  else
    echo "FAIL clean tree exited non-zero: $out"; fail=1
  fi
}

check_forbidden() {
  local path="$1"
  new_repo
  mkdir -p "$repo/$(dirname "$path")"
  echo x > "$repo/$path"
  git -C "$repo" add -f "$path"
  local rc=0
  out="$(cd "$repo" && bash scripts/check-no-secrets.sh 2>&1)" || rc=$?
  if [[ $rc -eq 1 && "$out" == *"$path"* ]]; then
    echo "ok   blocks $path"
  else
    echo "FAIL $path: exit $rc, output: $out"; fail=1
  fi
}

check_clean

# Allowed look-alikes must not be blocked.
new_repo
mkdir -p "$repo/docs" "$repo/internal/memorystore"
echo x > "$repo/docs/architecture.md"; echo x > "$repo/internal/memorystore/a.go"
echo x > "$repo/MYCLAUDE.md";
git -C "$repo" add -A .
if (cd "$repo" && bash scripts/check-no-secrets.sh >/dev/null 2>&1); then
  echo "ok   look-alike paths are allowed"
else
  echo "FAIL look-alike paths were blocked"; fail=1
fi

for p in CLAUDE.md AGENTS.md .claude/settings.json .agents/a.md .codex/a.md \
  .beads/a.db memory/MEMORY.md .superpowers/sdd/plan.md docs/superpowers/spec.md \
  test/api.key test/access.token; do
  check_forbidden "$p"
  check_forbidden "sub/dir/$p"
done

exit "$fail"
