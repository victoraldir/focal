# Releasing focal

Releases are cut by pushing a semver tag. A GitHub Actions workflow
(`.github/workflows/release.yml`) then runs `goreleaser release --clean`, which:

1. Builds `darwin/arm64` and `darwin/amd64` binaries.
2. Creates a GitHub Release with the archives and `checksums.txt`.
3. Generates a Homebrew **cask** and pushes it to the tap repository.

## One-time setup

### 1. Create the Homebrew tap repository

Create a **public** repo named exactly `homebrew-tap` under your account:

```
github.com/victoraldir/homebrew-tap
```

The `homebrew-` prefix is what lets users run `brew tap victoraldir/tap`
(Homebrew strips the prefix). It can start empty — GoReleaser creates the
`Casks/` directory and `focal.rb` on the first release.

### 2. Create a Personal Access Token (PAT) for the tap

GoReleaser needs to push the cask to a *different* repo than the one it's
releasing, so the default `GITHUB_TOKEN` isn't enough.

- **Classic PAT:** create one with the `repo` scope.
- **Fine-grained PAT (preferred):** grant access only to `homebrew-tap` with
  **Contents: Read and write**.

Copy the token value.

### 3. Store the PAT as an Actions secret on the `focal` repo

In `github.com/victoraldir/focal`:

> Settings → Secrets and variables → Actions → New repository secret

- **Name:** `HOMEBREW_TAP_TOKEN`
- **Value:** the PAT from step 2

The workflow already maps this secret into the `HOMEBREW_TAP_TOKEN` environment
variable that `.goreleaser.yaml` references.

## Cutting a release

```bash
# Ensure main is green and up to date, then:
git tag v0.1.0
git push origin v0.1.0
```

Watch the run under the repo's **Actions** tab. On success you'll have:

- A GitHub Release at `focal/releases/tag/v0.1.0` with macOS archives.
- An updated `Casks/focal.rb` committed to `homebrew-tap`.

Users can then install:

```bash
brew tap victoraldir/tap
brew install --cask focal
```

Or upgrade:

```bash
brew update && brew upgrade focal
```

## Validating locally before tagging

Install GoReleaser (`brew install goreleaser`) and run:

```bash
goreleaser check                        # validate .goreleaser.yaml
goreleaser release --snapshot --clean   # full local build, no publish
```

The `--snapshot` build writes artifacts to `dist/` without touching GitHub or
the tap, which is the safest way to confirm the config before pushing a tag.
