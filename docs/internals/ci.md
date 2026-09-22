# CI quality gates

> For maintainers. Using AgentTunnel? See [docs/user](../user/).

[`.github/workflows/ci.yml`](../../.github/workflows/ci.yml) runs these quality gates on pull requests
and pushes to `main`:

- **Check**: `vp check` (format and lint; this repo sets `typeCheck: false` in its lint options),
  then `vpr typecheck` for the workspace type check. The same job
  builds the desktop pipeline (`vp run build:desktop`) and verifies the preload bundle exists and
  uses only imports that Electron's sandbox can load. The verifier parses imports, then executes the
  trusted artifact with controlled bridge stubs to confirm that its required APIs are callable.
- **Test**: `vp run test` across the workspace.
- **Mobile Native Static Analysis**: `vp run lint:mobile` on macOS, wrapping
  `scripts/mobile-native-static-check.ts`. A cheap Linux **Mobile Native Changes** job gates it:
  the macOS runner only boots when the diff touches `apps/mobile` Swift/Kotlin sources, the
  SwiftLint/detekt/ktlint configuration, the `Brewfile`, the check script, the root `package.json`
  that defines `lint:mobile`, or `ci.yml`. Otherwise the job is skipped, which GitHub reports as
  success for the required check. Renames are matched on both their old and new path. The gate fails
  open in every other case: if the changed-file list cannot be resolved, GitHub truncates it, or the
  gate job itself fails, the lint runs.
- **Release Smoke**: exercises release-only workflow steps through `scripts/release-smoke.ts`, so
  release breakage surfaces on PRs rather than at tag time.

`.github/workflows/release.yml` builds macOS (`arm64` and `x64`), Linux (`x64`), and Windows (`x64`)
desktop artifacts from a single `v*.*.*` tag and publishes one GitHub release. It auto-enables
signing only when platform credentials are present. macOS passkey builds additionally require
`APPLE_TEAM_ID` and the `MACOS_PROVISIONING_PROFILE` secret; Windows uses Azure Trusted Signing.
Without the core signing credentials, it still releases unsigned artifacts.

See [Release Checklist](../operations/release.md) for the full release/signing setup checklist.

## Runners

Every job in `.github/workflows/` runs on [Depot](https://depot.dev) GitHub Actions runners, unless
the job's own comment says otherwise:

| Label                | Used for                                                                   | Capacity                        |
| -------------------- | -------------------------------------------------------------------------- | ------------------------------- |
| `depot-ubuntu-24.04` | every Linux job                                                            | 2 vCPU / 8 GB / 100 GB disk     |
| `depot-macos-14`     | mobile native static analysis, desktop macOS preview, iOS showcase capture | 8 vCPU M2 / 24 GB / 400 GB disk |
| `depot-windows-2025` | the Windows desktop artifact in `agenttunnel-release.yml`                  | 2 vCPU / 8 GB / 100 GB disk     |

Sizes match what these workflow jobs had on GitHub-hosted runners: `ubuntu-24.04` is 2 vCPU / 8 GB
for private repositories and `macos-14` is 3 vCPU M1 / 7 GB. When a job needs more, pick a larger
Depot size on that job only (`depot-ubuntu-24.04-4`, `depot-ubuntu-24.04-8`, …); see
[runner types](https://depot.dev/docs/github-actions/runner-types).

Constraints that shape the workflows:

- **Depot runners are organization-only.** The repository has to live under a GitHub organization
  that is connected to Depot (Depot app installed, repository allowed in the runner group). A
  personal-account repository cannot schedule these labels — its jobs stay queued with no runner.
- **One label per job.** Depot asks for a single `runs-on` label; label arrays such as
  `[self-hosted, depot-ubuntu-24.04]` are not supported.
- **No `/dev/kvm`.** Depot runner instances don't expose nested virtualization, so the Android
  emulator job in `mobile-showcase-screenshots.yml` stays on GitHub-hosted `ubuntu-24.04`. Depot CI
  sandboxes do expose KVM, which is the path if that job ever needs to move.
- **Windows runners have no Hyper-V**, so they can't run Docker-based steps; the Windows release job
  only builds NSIS installers.
- **macOS capacity is not fully elastic** — queues are longer than Linux during demand spikes, so
  don't park new required checks there.
- Job containers (`publish-aur.yml` runs in `archlinux:base-devel`) are supported: the runner image
  ships Docker, and Depot's own guides run jobs in containers on these runners.
- Runner startup costs 10–45 seconds before a job begins, and billing starts when the job starts, so
  a one-second job costs a few seconds rather than a minute.

One job is intentionally exempt from the Depot swap (the Android emulator job above); it carries a
comment saying why. Keep that pattern when a job needs something the runners can't provide.

Container image builds belong on Depot's own actions rather than `docker build` once this repo has an
image to build: `depot/setup-action@v1` to install the CLI, then `depot/build-push-action@v1` (or
`depot/bake-action@v1` for several images) with the Depot project id. Authentication is OIDC —
`permissions: id-token: write` and `contents: read`, no token secret — with a `DEPOT_TOKEN` secret
only where OIDC is unavailable, such as Dependabot workflows.
