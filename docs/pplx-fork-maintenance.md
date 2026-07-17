# Perplexity Atlantis fork maintenance

This document describes how to keep `banuchka-ppl/atlantis` synchronized with
`runatlantis/atlantis` while maintaining the Perplexity patch series and custom
release tags. It is specific to the Perplexity fork and should remain on its
integration branches.

## Branch and tag roles

| Ref | Purpose |
| --- | --- |
| `main` | Exact mirror of upstream `main`. Never add Perplexity commits here. |
| `pplx/integration` | Latest reviewed Perplexity patch series. Routine fork changes target this branch. |
| `pplx/integration-vX.Y` | Versioned integration line used to review a port to a new upstream baseline. |
| `pplx/port-vX.Y` | Temporary branch used to replay the patch series onto a new baseline. |
| `vX.Y.Z-pplx.N` | Immutable custom release. Never move or reuse these tags. |

Released history remains reachable through the custom release tags even when
`pplx/integration` is promoted to a newer upstream baseline.

## Configure the remotes

The fork should be `origin` and the Atlantis project should be `upstream`:

```bash
git remote add upstream https://github.com/runatlantis/atlantis.git
git remote -v
```

Skip `git remote add` when `upstream` already exists.

## Keep fork `main` synchronized

The simplest remote-only sync uses the GitHub CLI:

```bash
gh repo sync banuchka-ppl/atlantis \
  --source runatlantis/atlantis \
  --branch main
```

To synchronize through a local clone instead:

```bash
git fetch upstream main --tags
git switch main
git merge --ff-only upstream/main
git push origin main
```

If the fast-forward fails, inspect the fork-only commits before doing anything
destructive:

```bash
git log --oneline --left-right upstream/main...origin/main
```

Only after confirming that `main` contains no changes that need to be preserved,
reset the remote fork with:

```bash
gh repo sync banuchka-ppl/atlantis \
  --source runatlantis/atlantis \
  --branch main \
  --force
```

Perplexity changes belong on `pplx/integration`, not on `main`.

## Make a routine fork change

When the upstream baseline is unchanged, branch from the current integration
line and open the pull request back to it:

```bash
git fetch origin
git switch --create pplx/my-change origin/pplx/integration

# Make and validate the change.

git push --set-upstream origin pplx/my-change
gh pr create \
  --repo banuchka-ppl/atlantis \
  --base pplx/integration \
  --head pplx/my-change
```

## Release a routine fork change

Tag routine releases only after the change has merged into `pplx/integration`.
Never create a Perplexity release from fork `main` or from an unmerged feature
branch.

Fetch the integration refs and release tags, then name the previous cumulative
release explicitly:

```bash
git fetch origin \
  '+refs/heads/pplx/integration*:refs/remotes/origin/pplx/integration*' \
  '+refs/tags/v*-pplx.*:refs/tags/v*-pplx.*'

PREVIOUS_TAG=v0.46.0-pplx.5
RELEASE_TAG=v0.46.0-pplx.6
RELEASE_COMMIT=origin/pplx/integration
```

The candidate must contain the previous numbered release. Create the tag
locally, run the same lineage verifier used by the publishing workflows, and
only then push it:

```bash
git merge-base --is-ancestor "${PREVIOUS_TAG}" "${RELEASE_COMMIT}"
git tag "${RELEASE_TAG}" "${RELEASE_COMMIT}"
scripts/verify-pplx-release-lineage.sh \
  "${RELEASE_TAG}" \
  "${RELEASE_COMMIT}"
git push origin "${RELEASE_TAG}"
```

Both release workflows run this verifier before publishing archives or
container images. A failed lineage check means the tag was created from the
wrong branch or omitted a numbered cumulative release; do not move or reuse a
tag that has already been pushed.

### v0.46.0-pplx.4 lineage exception

`v0.46.0-pplx.4` was created from fork `main` and does not contain the
`v0.46.0-pplx.1` through `v0.46.0-pplx.3` patch series. It remains immutable
for auditability but must not be used as a cumulative base. `v0.46.0-pplx.5`
supersedes it, so the intentional cumulative ancestry is
`v0.46.0-pplx.3` → `v0.46.0-pplx.5`.

## Port the patch series to a new upstream baseline

Prefer a stable upstream tag for a production release. Use `upstream/main` when
early compatibility feedback is more important than a stable release base.

The following example ports the current `v0.44.1`-based patch series to upstream
`v0.46.0`:

```bash
git fetch upstream main --tags
git fetch origin

OLD_BASE=v0.44.1
NEW_BASE=v0.46.0
LINE=v0.46

git switch --create "pplx/port-${LINE}" "${NEW_BASE}"
```

For fresh upstream `main`, set `NEW_BASE=upstream/main` instead.

List the Perplexity commits in their original order:

```bash
git rev-list \
  --reverse \
  --no-merges \
  "${OLD_BASE}..origin/pplx/integration"
```

Review the output and cherry-pick those commit hashes explicitly, in order:

```bash
git cherry-pick COMMIT_1 COMMIT_2 COMMIT_3
```

Do not cherry-pick the pull request merge commits. Explicit replay is preferred
over rebasing `pplx/integration` because the integration branch contains merge
commits and already has release tags derived from its history.

Resolve conflicts one commit at a time. Continue with `git cherry-pick
--continue`, or abandon the candidate with `git cherry-pick --abort`; neither
operation changes the existing integration branch or releases.

## Verify the port

Use `range-diff` to compare the old and new patch series:

```bash
git range-diff \
  "${OLD_BASE}..origin/pplx/integration" \
  "${NEW_BASE}..pplx/port-${LINE}"
```

Every intentional change in patch behavior should be understood and called out
in the port pull request. Then run the normal fork validation:

```bash
make test
make check-fmt
make build-service
```

## Review the new integration line

Create a versioned integration branch at the pristine upstream baseline, then
open the port as a pull request against it:

```bash
git push origin \
  "${NEW_BASE}:refs/heads/pplx/integration-${LINE}"
git push --set-upstream origin "pplx/port-${LINE}"

gh pr create \
  --repo banuchka-ppl/atlantis \
  --base "pplx/integration-${LINE}" \
  --head "pplx/port-${LINE}"
```

This keeps the review diff limited to the Perplexity patch series instead of
mixing in all upstream changes since the previous baseline.

## Release and promote the port

After the port pull request merges, tag the merged versioned integration branch:

```bash
git fetch origin
RELEASE_TAG=v0.46.0-pplx.1
RELEASE_COMMIT="origin/pplx/integration-${LINE}"

git tag "${RELEASE_TAG}" "${RELEASE_COMMIT}"
scripts/verify-pplx-release-lineage.sh \
  "${RELEASE_TAG}" \
  "${RELEASE_COMMIT}"
git push origin "${RELEASE_TAG}"
```

Pushing the tag starts the fork release workflow. Confirm that the release and
required archives were created before downstream consumers adopt the version.

After the release is immutable and verified, promote the generic integration
branch to the new line:

```bash
git fetch origin
git push --force-with-lease origin \
  "origin/pplx/integration-${LINE}:refs/heads/pplx/integration"
```

`--force-with-lease` prevents overwriting integration work that appeared after
the last fetch. Keep the versioned integration branch and release tag so the old
and new release lines remain easy to inspect.

## Invariants

- Fork `main` always mirrors upstream.
- All custom behavior is reviewed on an integration branch.
- Ports replay only Perplexity commits onto a known upstream commit or tag.
- Released and versioned integration branches are not rebased or rewritten.
- Custom release tags are immutable.
- Except for the documented `v0.46.0-pplx.4` exception, each numbered custom
  release contains the preceding cumulative release and is reachable from a
  reviewed integration branch.
- A generic integration pointer is promoted only after the new release is
  verified.
