#!/usr/bin/env bash

set -euo pipefail

release_tag="${1:-}"
release_commit="${2:-}"
remote="${3:-origin}"

if [[ ! "${release_tag}" =~ ^(v[0-9]+\.[0-9]+\.[0-9]+)-pplx\.([1-9][0-9]*)$ ]]; then
    echo "expected a Perplexity release tag such as v0.46.0-pplx.6, got: ${release_tag:-<empty>}" >&2
    exit 1
fi

release_line="${BASH_REMATCH[1]}"
release_number="${BASH_REMATCH[2]}"

if [[ -z "${release_commit}" ]]; then
    echo "expected the release commit as the second argument" >&2
    exit 1
fi

tag_commit="$(git rev-parse --verify "refs/tags/${release_tag}^{commit}")"
candidate_commit="$(git rev-parse --verify "${release_commit}^{commit}")"

if [[ "${tag_commit}" != "${candidate_commit}" ]]; then
    echo "release tag ${release_tag} points to ${tag_commit}, not ${candidate_commit}" >&2
    exit 1
fi

if ((release_number > 1)); then
    previous_tag="${release_line}-pplx.$((release_number - 1))"

    if ! git rev-parse --verify --quiet "refs/tags/${previous_tag}^{commit}" >/dev/null; then
        echo "previous release tag ${previous_tag} does not exist" >&2
        exit 1
    fi

    if ! git merge-base --is-ancestor "${previous_tag}" "${candidate_commit}"; then
        echo "release ${release_tag} does not contain previous release ${previous_tag}" >&2
        exit 1
    fi
fi

integration_ref=""
while IFS= read -r ref; do
    if git merge-base --is-ancestor "${candidate_commit}" "${ref}"; then
        integration_ref="${ref}"
        break
    fi
done < <(git for-each-ref --format='%(refname)' "refs/remotes/${remote}/pplx/integration*")

if [[ -z "${integration_ref}" ]]; then
    echo "release ${release_tag} is not contained in a ${remote}/pplx/integration branch" >&2
    exit 1
fi

echo "verified ${release_tag} at ${candidate_commit} on ${integration_ref}"
