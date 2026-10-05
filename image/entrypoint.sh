#!/bin/sh
set -eu

# The instance starts this script as root with the work volume owned by root.
# Jobs run as an unprivileged user with sudo, as on GitHub-hosted runners.
# ACTIONS_RUNNER_INPUT_JITCONFIG passes through in the environment.
chown runner:runner /home/runner/_work

# The image only ships Node.js 24, so run Node.js 20 actions and the runner's
# own scripts on it.
export FORCE_JAVASCRIPT_ACTIONS_TO_NODE24=true
export ACTIONS_RUNNER_FORCED_INTERNAL_NODE_VERSION=node24

exec setpriv --reuid=runner --regid=runner --init-groups \
  env HOME=/home/runner USER=runner /home/runner/run.sh
