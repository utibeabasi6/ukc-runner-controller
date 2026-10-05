#!/bin/sh
set -eu

# unikraft build makes root the owner of every file in the rootfs, and only
# root can write to the runner's install directory, so the runner runs as root.
# The runner refuses that unless RUNNER_ALLOW_RUNASROOT is set.
# ACTIONS_RUNNER_INPUT_JITCONFIG passes through in the environment.
export RUNNER_ALLOW_RUNASROOT=1
export HOME=/root

# The image only ships Node.js 24, so run Node.js 20 actions and the runner's
# own scripts on it.
export FORCE_JAVASCRIPT_ACTIONS_TO_NODE24=true
export ACTIONS_RUNNER_FORCED_INTERNAL_NODE_VERSION=node24

exec /home/runner/run.sh
