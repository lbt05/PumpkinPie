#!/usr/bin/env bash
# render-unit.sh — render a pumpkinPie systemd unit with current paths.
# Shared by hack/get.sh and contrib/systemd/install.sh so the two
# install paths never drift on path substitution.
#
# Usage:
#   PP_BIN=... PP_DATA_DIR=... PP_STATE_DIR=... PP_HTTP=... PP_GRPC=... \
#     PP_MASTER_ADDR=... PP_NAME=... \
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
PP_DATA_DIR="${PP_DATA_DIR:-/var/lib/pp}"
PP_STATE_DIR="${PP_STATE_DIR:-/var/lib/pp-agent}"
PP_HTTP="${PP_HTTP:-0.0.0.0:8080}"
PP_GRPC="${PP_GRPC:-0.0.0.0:7000}"
PP_MASTER_ADDR="${PP_MASTER_ADDR:-pp-master.internal:7000}"
PP_NAME="${PP_NAME:-%H}"

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
      -e "s|/var/lib/pp|$PP_DATA_DIR|g" \
      -e "s|--http=0.0.0.0:8080|--http=$PP_HTTP|g" \
      -e "s|--grpc=0.0.0.0:7000|--grpc=$PP_GRPC|g" \
      "$TPL"
else
  sed -e "s|/usr/local/bin/pp|$PP_BIN|g" \
      -e "s|--state-dir=/var/lib/pp-agent|--state-dir=$PP_STATE_DIR|g" \
      -e "s|--master=pp-master.internal:7000|--master=$PP_MASTER_ADDR|g" \
      -e "s|--name=%H|--name=$PP_NAME|g" \
      "$TPL"
fi
