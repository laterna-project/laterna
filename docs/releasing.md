# Branches, versions and releases

This page is for maintainers. Contributors only need the short version in
[CONTRIBUTING.md](../CONTRIBUTING.md#branches).

## Branches

Laterna follows git-flow.

| Branch | Holds | Receives |
|---|---|---|
| `main` | released code; every commit on it is a release, tagged `vX.Y.Z` | `release/*` and `hotfix/*`, as merge commits |
| `develop` | the next release; the default branch | pull requests, squashed; `main` after each release |
| `feature/*`, `fix/*`, `docs/*`, `ci/*` | one change | branched from `develop`, merged back by pull request |
| `release/X.Y.Z` | a release being stabilized | fixes only |
| `hotfix/X.Y.Z` | an urgent fix to the latest release | branched from `main` |

On `develop`, a pull request is one squashed commit whose subject starts with a gitmoji. The only
merge commits there are the ones that bring a release back from `main`.
`main` only moves when a version is released.

The Docker image `edge` is built from `develop`. `latest` and the version tags come from release
tags.

## Versions

Versions follow [Semantic Versioning](https://semver.org/). What counts as the public interface:

- the API contract (`proto/laterna/v1`);
- the startup configuration (TOML keys, `LATERNA_*` variables, command line);
- the on-disk layout that users touch (backup names, folders);
- metric names.

Since `1.0`, a minor version only adds to these, a patch version only fixes, and a breaking
change needs a major version. (Before `1.0`, a minor version could change any of them; the
release notes of each `0.x` say how to upgrade.)

Two things are stricter than SemVer asks:

- **The contract never breaks within `laterna.v1`.** `buf breaking` enforces it on every pull
  request. A change that cannot be made compatibly goes in a new package.
- **The database only moves forward.** A release can always open the database of any earlier
  release, and backs it up before migrating. Going back means restoring that backup.

The version is not written in any file. Builds read it from the tag: `laterna version` prints the
tag for a release, and `git describe` output for anything else.

## Cutting a release

```sh
# 1. Branch from develop. From here on, develop is open for the next version.
git switch develop && git pull
git switch -c release/1.1.0
git push -u origin release/1.1.0

# 2. Stabilize: only fixes, each by pull request into release/1.1.0.
#    Optional release candidate, built and published as a pre-release:
git tag -a v1.1.0-rc.1 -m "v1.1.0-rc.1" && git push origin v1.1.0-rc.1

# 3. Release: merge into main with a merge commit, then tag main.
gh pr create --base main --head release/1.1.0 --title "🔖 Release 1.1.0" --body ""
gh pr merge --merge
git switch main && git pull
git tag -a v1.1.0 -m "v1.1.0" && git push origin v1.1.0

# 4. Bring the fixes back to develop.
gh pr create --base develop --head main --title "🔀 Merge release 0.2.0 into develop" --body ""
gh pr merge --merge
```

The back-merge in step 4 is the one pull request into `develop` that is not squashed: the merge
commit is what tells git that `develop` contains the release, so that `git describe` there counts
from the new tag.

Pushing the tag starts the [release workflow](../.github/workflows/release.yml), which:

1. checks that the tag points at a commit of `main` (pre-release tags are exempt);
2. downloads the pinned FFmpeg builds;
3. builds every archive and package with GoReleaser and uploads them to a **draft** release;
4. unpacks the Linux bundle and installs the deb package on the runner, and checks that the
   server starts and finds its FFmpeg;
5. attests the provenance of the files;
6. publishes the release;
7. builds, smoke tests and pushes the Docker image for the version.

If a step fails, the draft stays unpublished: fix the problem, delete the draft and the tag, and
tag again.

To see what a release would contain without publishing anything, run the workflow by hand
(`gh workflow run release.yml`): it builds a snapshot and runs the same checks.

## Hotfix

```sh
git switch main && git pull
git switch -c hotfix/1.1.1
# fix, push, then the same steps 3 and 4 as a release, with v1.1.1
```

## Updating FFmpeg

The FFmpeg build is pinned in one place, [`packaging/ffmpeg.lock`](../packaging/ffmpeg.lock):
the release name, the file name and the SHA-256 of each platform's archive, all from the
[BtbN releases](https://github.com/BtbN/FFmpeg-Builds/releases). The Docker image, CI and the
release bundles read it through `packaging/fetch-ffmpeg.sh`. Change the pin in its own pull
request: the whole test suite then runs against the new build.

BtbN deletes its daily builds after about two weeks. Once a pin reaches `develop` or `main`, the
[FFmpeg mirror workflow](../.github/workflows/ffmpeg-mirror.yml) copies its archives, checked
against their SHA-256, to the `ffmpeg-builds` pre-release of this repository
(`packaging/mirror-ffmpeg.sh`). `fetch-ffmpeg.sh` downloads from there first and only falls back
to BtbN for a pin that has not been copied yet, such as the one of an open pull request. Copies
are never deleted: every release can still be rebuilt with its own FFmpeg. The workflow also runs
every day, to retry a copy that failed; run it by hand with `gh workflow run ffmpeg-mirror.yml`.

## Updating the web client

The Docker image serves a release of [Laterna Web](https://github.com/laterna-project/laterna-web),
pinned in [`packaging/web.lock`](../packaging/web.lock) by version and SHA-256. Move the pin in its
own pull request:

```sh
sh packaging/update-web.sh 0.10.0
```

The script takes the SHA-256 from the release's `checksums.txt`. CI then builds the image with that
client and its smoke test checks that the page and its script load. The weekly workflow says when
a newer client is released.

Laterna Web is generated from a tagged server release, so a release whose image needs a new client
goes in this order: tag a release candidate on the release branch, generate and release the client
from it, pin that client on the release branch, then tag the release. That is how 1.0.0 shipped with
Laterna Web 0.10.0.

## Repository settings

Branch and tag rules are kept in [`.github/rulesets/`](../.github/rulesets) and applied with:

```sh
for f in .github/rulesets/*.json; do
  gh api --method POST repos/laterna-project/laterna/rulesets --input "$f"
done
```

- `develop` and `main`: no direct push, no force push, pull request with green checks required.
  `main` only accepts merge commits. `develop` accepts squash merges, and merge commits for the
  back-merge of a release.
- `v*` tags: created by administrators only, never moved or deleted.
- Squash and merge commits are allowed, rebase merges are not, and merged branches are deleted.
- Workflows get a read-only token by default; each job asks for what it needs.
