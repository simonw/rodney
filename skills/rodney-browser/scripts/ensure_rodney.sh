#!/bin/bash
# Ensure rodney binary is available and Chrome is running.
# Usage: source scripts/ensure_rodney.sh [--build-dir /path/to/rodney/source]
#
# Sets RODNEY variable to the path of the rodney binary.
# Starts Chrome if not already running.

set -e

BUILD_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --build-dir) BUILD_DIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done

# Find rodney binary
if command -v rodney &>/dev/null; then
  RODNEY="$(command -v rodney)"
elif [ -n "$BUILD_DIR" ] && [ -f "$BUILD_DIR/rodney" ]; then
  RODNEY="$BUILD_DIR/rodney"
elif [ -n "$BUILD_DIR" ] && [ -f "$BUILD_DIR/main.go" ]; then
  echo "Building rodney from source at $BUILD_DIR ..."
  (cd "$BUILD_DIR" && go build -o rodney .) || { echo "ERROR: go build failed"; exit 1; }
  RODNEY="$BUILD_DIR/rodney"
else
  echo "ERROR: rodney binary not found. Install rodney and ensure it is on your PATH, or pass --build-dir."
  exit 1
fi

export RODNEY

# Start Chrome if not already running
if ! "$RODNEY" status &>/dev/null; then
  echo "Starting Chrome via rodney..."
  "$RODNEY" start
fi

echo "Rodney ready: $RODNEY"
echo "Status: $("$RODNEY" status)"
