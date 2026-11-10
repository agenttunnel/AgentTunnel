#!/usr/bin/env bash
#
# Builds the iOS app with ios-build.sh, then opens a PR from the current branch
# to main. Dry-run by default: it prints what it would do and changes nothing
# remote. Pass --create to push the branch and open the PR.
#
# A PR needs a conventional title and a body file written by you, covering the
# problem, the fix, and the model and harness that did the work (AGENTS.md).
# The Claude Code attribution line is appended for you. The dry run may default
# the title to the branch's latest commit subject, but --create requires an
# explicit --title, because that subject does not describe the whole PR.
#
# A branch more than one commit ahead of main carries all of those commits into
# the PR. Branch from main first for an iOS-only PR; the script warns otherwise.
#
# Usage:
#   scripts/ios-pr.sh                          # dry run
#   scripts/ios-pr.sh --body-file body.md      # dry run with body preview
#   scripts/ios-pr.sh --create --title "ci(mobile): ..." --body-file body.md
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"

MODE=dry-run
TITLE=""
BODY_FILE=""

fail() {
  echo "ios-pr: $*" >&2
  exit 1
}

usage() {
  sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=dry-run ;;
    --create) MODE=create ;;
    --title) [ $# -ge 2 ] || fail "--title needs a value"; TITLE="$2"; shift ;;
    --body-file) [ $# -ge 2 ] || fail "--body-file needs a path"; BODY_FILE="$2"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument: $1 (see --help)" ;;
  esac
  shift
done

if [ "$MODE" = create ]; then
  [ -n "$TITLE" ] || fail "--create needs --title; the default title is the latest commit subject, which does not describe the whole PR"
  [ -n "$BODY_FILE" ] || fail "--create needs --body-file"
fi

repo="$(git -C "$here" rev-parse --show-toplevel)"
branch="$(git -C "$repo" branch --show-current)"
[ -n "$branch" ] || fail "detached HEAD; check out a branch first"
[ "$branch" != "main" ] || fail "refusing to open a PR from main"

# Build first. A failing build stops here, before any push.
"$here/ios-build.sh"

if [ -n "$(git -C "$repo" status --porcelain)" ]; then
  fail "working tree has uncommitted changes; commit them first (this script does not commit)"
fi

base=main
git -C "$repo" rev-parse --verify -q origin/main >/dev/null && base=origin/main
ahead="$(git -C "$repo" rev-list --count "$base"..HEAD)"
if [ "$ahead" -eq 0 ]; then
  echo "ios-pr: $branch has no commits beyond $base; nothing to open a PR for"
  exit 0
fi
if [ "$ahead" -gt 1 ]; then
  echo "ios-pr: warning: $branch is $ahead commits beyond $base, so the PR carries non-iOS commits too." >&2
  echo "ios-pr: for an iOS-only PR, branch from main first (one concern per PR)." >&2
fi

if [ -z "$TITLE" ]; then
  TITLE="$(git -C "$repo" log -1 --format=%s)"
fi
conventional='^(feat|fix|docs|test|chore|refactor|perf|build|ci|style|revert)(\([a-z0-9._/-]+\))?!?: .+$'
[[ "$TITLE" =~ $conventional ]] || fail "title is not a conventional commit title: '$TITLE' (pass --title)"

if [ -z "$BODY_FILE" ]; then
  echo "ios-pr: no --body-file given; a real run needs one (problem, fix, model and harness)"
else
  [ -r "$BODY_FILE" ] || fail "cannot read body file: $BODY_FILE"
fi

echo
echo "PR plan"
echo "  base:   main"
echo "  head:   $branch ($ahead commit(s) beyond $base)"
echo "  title:  $TITLE"
echo "  body:   ${BODY_FILE:-<none>}"

if [ "$MODE" = "dry-run" ]; then
  echo
  echo "dry run: nothing pushed, no PR opened. Would run:"
  echo "  git push -u origin $branch"
  echo "  gh pr create --base main --head $branch --title <title> --body-file <body + attribution>"
  echo "Re-run with --create to do it."
  exit 0
fi

command -v gh >/dev/null 2>&1 || fail "gh not found on PATH"
gh auth status >/dev/null 2>&1 || fail "gh is not authenticated; run 'gh auth login'"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/agenttunnel-pr.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
full_body="$tmp/body.md"
cp "$BODY_FILE" "$full_body"
printf '\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)\n' >> "$full_body"

git -C "$repo" push -u origin "$branch"
gh pr create --base main --head "$branch" --title "$TITLE" --body-file "$full_body"
