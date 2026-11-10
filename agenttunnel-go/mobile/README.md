# AgentTunnel mobile

Expo app that pairs with a desktop AgentTunnel through the relay.

## iOS build and PR

Requires macOS with full Xcode (Command Line Tools alone are not enough), Node,
and Bun.

```sh
scripts/ios-build.sh
```

This runs `npm ci`, `tsc --noEmit`, the Bun smoke checks, and `expo prebuild
--platform ios --no-install`, then an unsigned `xcodebuild` for
`iphonesimulator`. The generated `ios/` directory is gitignored and rebuilt on
each run. Derived data goes to a temp directory.

To open a PR from the current branch:

```sh
scripts/ios-pr.sh                                  # dry run: prints the plan
scripts/ios-pr.sh --create --body-file body.md     # builds, pushes, opens the PR
```

`--create` needs a body file that describes the problem, the fix, and the model
and harness that did the work. The script appends the Claude Code attribution
line. The title defaults to the branch's latest commit subject, which must be a
conventional commit title (or pass `--title`). The script never commits.

CI runs the same build in `.github/workflows/agenttunnel-ios.yml` on changes
under `agenttunnel-go/mobile/`. It uses no secrets and no signing.
