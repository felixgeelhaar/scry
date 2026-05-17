#!/usr/bin/env bash
# Block direct pushes to protected branches. Install via:
#   make install-hooks
#
# Override for documented incident workflows:
#   ALLOW_PROTECTED_PUSH=1 git push origin main

set -euo pipefail

protected_branches=("main" "master" "release")

while read -r local_ref local_sha remote_ref remote_sha; do
    : "$local_sha" "$remote_sha"
    remote_branch="${remote_ref##refs/heads/}"
    for protected in "${protected_branches[@]}"; do
        if [[ "$remote_branch" == "$protected" ]]; then
            if [[ "${ALLOW_PROTECTED_PUSH:-}" == "1" ]]; then
                echo "scry pre-push: ALLOW_PROTECTED_PUSH=1 set; allowing push to $remote_branch" >&2
                exit 0
            fi
            cat >&2 <<EOF
scry pre-push: refusing direct push to protected branch '$remote_branch'.

Open a pull request instead. For incident-response overrides:
  ALLOW_PROTECTED_PUSH=1 git push origin $remote_branch
EOF
            exit 1
        fi
    done
done

exit 0
