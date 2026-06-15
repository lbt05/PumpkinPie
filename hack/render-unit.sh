#!/usr/bin/env bash
# render-unit.sh — render a pumpkinPie systemd unit with current paths.
# Shared by hack/get.sh and contrib/systemd/install.sh so the two
# install paths never drift on path substitution.
#
# Usage:
#   PP_BIN=... PP_CONFIG=... PP_DATA_DIR=... PP_STATE_DIR=... \
#     PP_AGENT_CONFIG=... PP_MASTER_ADDR=... \
#     render-unit.sh <role> [<template-file>]
#
# Defaults match hack/get.sh and the existing contrib units.
# Prints the rendered unit on stdout; returns non-zero on error.

set -euo pipefail

ROLE="${1:-}"
TPL="${2:-}"

[[ "$ROLE" == "master" || "$ROLE" == "agent" ]] || {
  echo "render-unit.sh: role must be 'master' or 'agent'" >&2
  exit 2
}

PP_BIN="${PP_BIN:-/usr/local/bin/pp}"
PP_CONFIG="${PP_CONFIG:-/etc/pp/pp-master.yaml}"
PP_AGENT_CONFIG="${PP_AGENT_CONFIG:-/etc/pp/pp-agent.yaml}"
PP_DATA_DIR="${PP_DATA_DIR:-/var/lib/pp}"
PP_STATE_DIR="${PP_STATE_DIR:-/var/lib/pp-agent}"

# Resolve template path if not provided.
if [[ -z "$TPL" ]]; then
  # Walk up from this script's location to find the template.
  SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
  for candidate in \
    "$SELF_DIR/../contrib/systemd/pp-${ROLE}.service" \
    "$SELF_DIR/../../contrib/systemd/pp-${ROLE}.service" \
    "./contrib/systemd/pp-${ROLE}.service"; do
    if [[ -f "$candidate" ]]; then TPL="$candidate"; break; fi
  done
fi
[[ -f "$TPL" ]] || { echo "render-unit.sh: template not found" >&2; exit 1; }

if [[ "$ROLE" == "master" ]]; then
  sed -e "s|/usr/local/bin/pp|$PP_BIN|g" \
      -e "s|/etc/pp/pp-master.yaml|$PP_CONFIG|g" \
      -e "s|/var/lib/pp|$PP_DATA_DIR|g" \
      "$TPL"
else
  sed -e "s|/usr/local/bin/pp|$PP_BIN|g" \
      -e "s|/etc/pp/pp-agent.yaml|$PP_AGENT_CONFIG|g" \
      -e "s|/var/lib/pp-agent|$PP_STATE_DIR|g" \
      "$TPL"
fi