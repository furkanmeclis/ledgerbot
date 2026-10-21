#!/usr/bin/env bash
# Registers this Mac as a self-hosted GitHub Actions runner for furkanmeclis/ledgerbot
# and installs it as a user LaunchAgent (starts at login).
#
#   ./scripts/setup-runner.sh            # install and start
#   ./scripts/setup-runner.sh remove     # stop, unregister and delete
#
# Switch CI back to GitHub-hosted runners at any time:
#   gh variable set CI_RUNS_ON --repo furkanmeclis/ledgerbot --body '"ubuntu-latest"'
# and back to this Mac:
#   gh variable delete CI_RUNS_ON --repo furkanmeclis/ledgerbot
set -euo pipefail
REPO=furkanmeclis/ledgerbot
DIR="$HOME/actions-runner/ledgerbot"

if [ "${1:-}" = "remove" ]; then
  cd "$DIR"
  ./svc.sh stop || true
  ./svc.sh uninstall || true
  TOKEN=$(gh api -X POST "repos/$REPO/actions/runners/remove-token" --jq .token)
  ./config.sh remove --token "$TOKEN"
  cd ~ && rm -rf "$DIR"
  echo "runner removed"
  exit 0
fi

mkdir -p "$DIR" && cd "$DIR"
if [ ! -x ./config.sh ]; then
  V=$(gh api repos/actions/runner/releases/latest --jq .tag_name | sed 's/^v//')
  curl -fsSL -o runner.tar.gz "https://github.com/actions/runner/releases/download/v$V/actions-runner-osx-arm64-$V.tar.gz"
  tar xzf runner.tar.gz && rm runner.tar.gz
fi
TOKEN=$(gh api -X POST "repos/$REPO/actions/runners/registration-token" --jq .token)
./config.sh --unattended --replace --url "https://github.com/$REPO" --token "$TOKEN" \
  --name "$(scutil --get LocalHostName)-ledgerbot" --labels ledgerbot --work _work
./svc.sh install
./svc.sh start
echo "runner installed; re-run failed CI with: gh run rerun --repo $REPO --failed <run-id>"
