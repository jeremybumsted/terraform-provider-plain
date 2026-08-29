#!/usr/bin/env bash
#
# End-to-end walkthrough against a real Plain workspace, driven through the
# real terraform binary.
#
# This is not the acceptance suite. That one drives the provider through
# terraform-plugin-testing, which never starts a terraform process — so it
# cannot show what a practitioner actually sees: diagnostic wording and
# wrapping, dev_overrides resolution, or the real plan/import/destroy cycle.
# This script is the automated form of the manual pre-release run.
#
# It creates and destroys real objects. PLAIN_API_KEY selects the workspace;
# point it at staging, never production.

set -euo pipefail

: "${PLAIN_API_KEY:?PLAIN_API_KEY must be set — point it at the staging workspace, never production}"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# Randomized and tf-acc-prefixed so a run that dies before cleanup leaves
# something the existing sweep can find.
name="tf-acc-e2e-$(date +%Y%m%d%H%M%S)-${RANDOM}"

work="$(mktemp -d)"
apply_dir="$work/apply"
import_dir="$work/import"
mkdir -p "$apply_dir" "$import_dir"
cp "$root/test/e2e/main.tf" "$apply_dir/main.tf"
cp "$root/test/e2e/main.tf" "$import_dir/main.tf"

export TF_VAR_name="$name"
export TF_IN_AUTOMATION=1

# Destroy runs even when a step above fails, otherwise a mid-run failure
# orphans a real workflow. CheckDestroy in the Go suite has the same gap and
# CLAUDE.md calls for a manual sweep after a failure; this at least narrows it.
cleanup() {
  local rc=$?
  trap - EXIT

  if [[ -f "$apply_dir/terraform.tfstate" ]]; then
    echo "--- :terraform: destroy"
    if ! terraform -chdir="$apply_dir" destroy -auto-approve -input=false; then
      echo "^^^ +++"
      echo "DESTROY FAILED — workflow '$name' may still exist in the staging workspace."
      echo "Sweep for tf-acc* workflows before the next run."
      rc=1
    fi
  fi

  rm -rf "$work"
  exit "$rc"
}
trap cleanup EXIT

echo "--- :hammer: build and install the provider"
mise run install
mise run dev-override
export TF_CLI_CONFIG_FILE="$root/.terraformrc-dev"

# Deliberately no `terraform init`: it fails for a provider under
# dev_overrides, by design. Terraform warns about the override and proceeds.
echo "--- :terraform: apply ($name)"
terraform -chdir="$apply_dir" apply -auto-approve -input=false

echo "--- :terraform: plan is clean"
rc=0
terraform -chdir="$apply_dir" plan -detailed-exitcode -input=false || rc=$?
if [[ $rc -ne 0 ]]; then
  echo "^^^ +++"
  echo "Expected no drift after apply, got exit $rc (2 means the plan is non-empty)."
  exit 1
fi

workflow_id="$(terraform -chdir="$apply_dir" output -raw workflow_id)"

echo "--- :terraform: import into a fresh directory"
terraform -chdir="$import_dir" import plain_workflow.e2e "$workflow_id"

# The post-import plan is EXPECTED to be non-empty. Plain does not store the
# local step keys, so imported steps come back keyed by their Plain step ID and
# the next plan proposes rekeying them — documented in
# examples/resources/plain_workflow/import.sh and asserted by
# TestAccWorkflow_import. Exit 0 here would mean that rekeying had silently
# stopped happening, which is a change worth failing on.
echo "--- :terraform: post-import plan shows the documented rekeying"
rc=0
terraform -chdir="$import_dir" plan -detailed-exitcode -input=false || rc=$?
if [[ $rc -ne 2 ]]; then
  echo "^^^ +++"
  echo "Expected the post-import plan to be non-empty (exit 2), got exit $rc."
  echo "Imported steps are keyed by Plain step ID and should replan as a rekey."
  exit 1
fi

echo "--- :white_check_mark: walkthrough passed"
