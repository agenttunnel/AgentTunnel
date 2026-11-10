# AgentTunnel mobile

Expo app that pairs with a desktop AgentTunnel through the relay.

## Pairing on your LAN

1. Run the relay where both devices can reach it, bound to the LAN:
   `atrelay -listen 0.0.0.0:7777`. It prints a `reachable on the LAN at ws://…`
   line with the address to use. The default `127.0.0.1` bind is loopback only.
2. In the desktop app's Settings, turn on the phone relay and use that
   `ws://` address. The pairing QR then points the phone at this machine's LAN
   address, and the saved URL is unchanged.
3. On the phone, tap **Scan QR code** and point the camera at the desktop QR.
   The scan fills in the relay address and code. Tap **Pair** to finish. Typing
   the code still works.

Plain `ws://` is accepted only for loopback and private addresses. Use `wss://`
behind TLS for anything on the internet.

## iOS build and PR

Requires macOS with full Xcode 26 or newer (Swift 6.2; Command Line Tools alone
are not enough, and older Xcode fails in ExpoModulesJSI), CocoaPods (`brew install cocoapods`), Node, and Bun.

```sh
scripts/ios-build.sh
```

This runs `npm ci`, `tsc --noEmit`, the Bun smoke checks, and `expo prebuild
--platform ios` (which installs CocoaPods dependencies into `ios/`), then an
unsigned `xcodebuild` for `iphonesimulator`. It builds the `.xcworkspace` that
prebuild produces, and falls back to the bare `.xcodeproj` only if there is no
workspace. The generated `ios/` directory is gitignored and rebuilt on each
run. Derived data goes to a temp directory.

The build has not yet been run on a Mac with Xcode; the first CI run on
`macos-15` is the first proof of the xcodebuild leg.

To open a PR from the current branch:

```sh
scripts/ios-pr.sh                                  # dry run: prints the plan
scripts/ios-pr.sh --create --body-file body.md     # builds, pushes, opens the PR
```

`--create` needs `--title` (a conventional commit title, for example
`ci(mobile): ...`) and a body file that describes the problem, the fix, and the
model and harness that did the work. The script appends the Claude Code
attribution line. The dry run defaults the title to the branch's latest commit
subject, but `--create` refuses to, because that subject does not describe the
whole PR. The script never commits.

A branch more than one commit ahead of `main` carries every one of those
commits into the PR. The script warns when that happens. For an iOS-only PR,
branch from `main` first.

CI runs the same build in `.github/workflows/agenttunnel-ios.yml` on pushes to
`main`, on pull requests, and on manual dispatch, filtered to changes under
`agenttunnel-go/mobile/`. It uses no secrets and no signing.
