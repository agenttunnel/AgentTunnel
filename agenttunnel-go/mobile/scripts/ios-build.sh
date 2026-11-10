#!/usr/bin/env bash
#
# Builds the AgentTunnel mobile app for the iOS simulator without signing.
#
# Steps: npm ci, typecheck, bun smoke checks, expo prebuild for iOS (which runs
# CocoaPods, so ios/ gets a Pods/ directory and an .xcworkspace), then an
# unsigned xcodebuild for iphonesimulator. The workspace is built when prebuild
# produced one; the bare .xcodeproj is only a fallback and cannot compile a
# React Native app. The generated ios/ directory is gitignored and rebuilt on
# every run.
#
# Requires macOS with full Xcode (not just Command Line Tools), CocoaPods
# (`brew install cocoapods`), Node, and Bun.
#
# Usage: scripts/ios-build.sh
set -euo pipefail

cd "$(dirname "$0")/.."

fail() {
  echo "ios-build: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 not found on PATH${2:+ ($2)}"
}

[ "$(uname -s)" = "Darwin" ] || fail "iOS builds need macOS (this is $(uname -s))"
need xcodebuild "install Xcode from the App Store"
need xcrun
need node
need npm
need bun "https://bun.sh"
command -v pod >/dev/null 2>&1 || fail "pod not found; brew install cocoapods"

# xcodebuild exists under Command Line Tools too, but it refuses to run there.
if ! xcodebuild -version >/dev/null 2>&1; then
  fail "xcodebuild needs full Xcode; run 'sudo xcode-select -s /Applications/Xcode.app' after installing it"
fi

# Derived data goes to a temp dir so the build never touches ~/Library/Developer.
DERIVED="$(mktemp -d "${TMPDIR:-/tmp}/agenttunnel-ios.XXXXXX")"
trap 'rm -rf "$DERIVED"' EXIT

echo "› npm ci"
npm ci

echo "› typecheck"
npx tsc --noEmit

echo "› smoke"
bun run scripts/smoke.ts

echo "› expo prebuild (ios, installs pods)"
npx expo prebuild --platform ios

shopt -s nullglob
projects=(ios/*.xcodeproj)
workspaces=(ios/*.xcworkspace)
shopt -u nullglob
[ "${#projects[@]}" -eq 1 ] || fail "expected exactly one ios/*.xcodeproj after prebuild, found ${#projects[@]}"
project="${projects[0]}"
scheme="$(basename "$project" .xcodeproj)"

# Prefer the CocoaPods workspace; the bare project cannot link pod dependencies.
if [ "${#workspaces[@]}" -eq 1 ]; then
  container=(-workspace "${workspaces[0]}")
else
  echo "ios-build: no single ios/*.xcworkspace after prebuild; falling back to the project" >&2
  container=(-project "$project")
fi

echo "› xcodebuild $scheme (iphonesimulator, unsigned, ${container[0]#-})"
xcodebuild \
  "${container[@]}" \
  -scheme "$scheme" \
  -sdk iphonesimulator \
  -configuration Debug \
  -derivedDataPath "$DERIVED" \
  CODE_SIGNING_ALLOWED=NO \
  build

echo "› done: iOS simulator build of $scheme succeeded"
