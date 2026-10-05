#!/bin/sh
set -eu

# The instance starts this script as root with the work volume owned by root.
# Jobs run as an unprivileged user with sudo, as on GitHub-hosted runners.
# ACTIONS_RUNNER_INPUT_JITCONFIG passes through in the environment.
chown runner:runner /home/runner/_work

exec setpriv --reuid=runner --regid=runner --init-groups \
  env HOME=/home/runner USER=runner /home/runner/run.sh
