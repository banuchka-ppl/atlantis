#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
verifier="${script_dir}/verify-pplx-release-lineage.sh"
test_repo="$(mktemp -d)"
trap 'rm -rf "${test_repo}"' EXIT

git -C "${test_repo}" init --quiet
git -C "${test_repo}" config user.email test@example.com
git -C "${test_repo}" config user.name "Release Lineage Test"

commit_change() {
    local message="$1"

    echo "${message}" >>"${test_repo}/history"
    git -C "${test_repo}" add history
    git -C "${test_repo}" commit --quiet --message "${message}"
}

expect_failure() {
    if "$@" >/dev/null 2>&1; then
        echo "expected command to fail: $*" >&2
        exit 1
    fi
}

commit_change "first release"
first_commit="$(git -C "${test_repo}" rev-parse HEAD)"
git -C "${test_repo}" tag v1.2.3-pplx.1
git -C "${test_repo}" update-ref refs/remotes/origin/pplx/integration "${first_commit}"
(
    cd "${test_repo}"
    "${verifier}" v1.2.3-pplx.1 "${first_commit}"
)

commit_change "second release"
second_commit="$(git -C "${test_repo}" rev-parse HEAD)"
git -C "${test_repo}" tag v1.2.3-pplx.2
git -C "${test_repo}" update-ref refs/remotes/origin/pplx/integration "${second_commit}"
(
    cd "${test_repo}"
    "${verifier}" v1.2.3-pplx.2 "${second_commit}"
    expect_failure "${verifier}" v1.2.3-pplx.2 "${first_commit}"
)

git -C "${test_repo}" checkout --quiet --detach "${first_commit}"
commit_change "broken third release"
broken_commit="$(git -C "${test_repo}" rev-parse HEAD)"
git -C "${test_repo}" tag v1.2.3-pplx.3
git -C "${test_repo}" update-ref refs/remotes/origin/pplx/integration "${broken_commit}"
(
    cd "${test_repo}"
    expect_failure "${verifier}" v1.2.3-pplx.3 "${broken_commit}"
)

git -C "${test_repo}" checkout --quiet --detach "${second_commit}"
commit_change "release outside integration"
outside_commit="$(git -C "${test_repo}" rev-parse HEAD)"
git -C "${test_repo}" tag v2.0.0-pplx.1
git -C "${test_repo}" update-ref refs/remotes/origin/pplx/integration "${second_commit}"
(
    cd "${test_repo}"
    expect_failure "${verifier}" v2.0.0-pplx.1 "${outside_commit}"
    expect_failure "${verifier}" invalid-tag "${outside_commit}"
)

echo "Perplexity release lineage checks passed"
