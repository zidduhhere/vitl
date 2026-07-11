#!/usr/bin/env bash
# mobile/build_android.sh
#
# Builds vitallink.aar from the mobile/vitallink Go package and places it
# under android/app/libs/ so the Gradle project can pick it up with:
#   implementation(files("libs/vitallink.aar"))
#
# Prerequisites:
#   - Android SDK at ~/Library/Android/sdk (or $ANDROID_HOME)
#   - NDK 28.2.13676358 installed (SDK Manager → SDK Tools → NDK)
#   - Homebrew openjdk@17 installed
#   - golang.org/x/mobile already in go.mod (added via:
#       go get -tool golang.org/x/mobile/cmd/gobind)
#
# Run from anywhere inside the repo.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
OUTPUT="$REPO_ROOT/android/app/libs/vitallink.aar"
MOBILE_PKG="./mobile/vitallink"

echo "==> VitalLink Android AAR build"
echo "    repo root : $REPO_ROOT"
echo "    output    : $OUTPUT"

# ---- 1. JAVA_HOME -----------------------------------------------------------
if command -v brew &>/dev/null; then
    BREW_JDK="$(brew --prefix openjdk@17 2>/dev/null || true)"
    if [[ -n "$BREW_JDK" && -d "$BREW_JDK/bin" ]]; then
        export JAVA_HOME="$BREW_JDK"
        export PATH="$JAVA_HOME/bin:$PATH"
        echo "    JAVA_HOME : $JAVA_HOME  (Homebrew openjdk@17)"
    fi
fi

if ! command -v javac &>/dev/null; then
    echo "ERROR: javac not found. Install openjdk@17 via 'brew install openjdk@17'." >&2
    exit 1
fi
echo "    javac     : $(javac -version 2>&1)"

# ---- 2. Android SDK / NDK ---------------------------------------------------
export ANDROID_HOME="${ANDROID_HOME:-$HOME/Library/Android/sdk}"
NDK_VERSION="28.2.13676358"
export ANDROID_NDK_HOME="${ANDROID_NDK_HOME:-$ANDROID_HOME/ndk/$NDK_VERSION}"

if [[ ! -d "$ANDROID_HOME" ]]; then
    echo "ERROR: Android SDK not found at $ANDROID_HOME" >&2
    exit 1
fi
if [[ ! -d "$ANDROID_NDK_HOME" ]]; then
    echo "ERROR: NDK $NDK_VERSION not found at $ANDROID_NDK_HOME" >&2
    echo "       Install via: Android Studio → SDK Manager → SDK Tools → NDK" >&2
    exit 1
fi
echo "    ANDROID_HOME     : $ANDROID_HOME"
echo "    ANDROID_NDK_HOME : $ANDROID_NDK_HOME"

# ---- 3. Ensure GOPATH/bin is on PATH ----------------------------------------
GOPATH_BIN="$(go env GOPATH)/bin"
export PATH="$GOPATH_BIN:$PATH"
echo "    GOPATH/bin: $GOPATH_BIN"

# ---- 4. Install gomobile + gobind to GOPATH/bin ----------------------------
# Use @latest so we get the same version that's declared in go.mod.
# This is idempotent — 'go install' is a no-op when already up-to-date.
echo "==> Installing gomobile and gobind ..."
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest

GOMOBILE="$GOPATH_BIN/gomobile"
GOBIND="$GOPATH_BIN/gobind"

if [[ ! -x "$GOMOBILE" ]]; then
    echo "ERROR: gomobile not found at $GOMOBILE after install." >&2
    exit 1
fi
if [[ ! -x "$GOBIND" ]]; then
    echo "ERROR: gobind not found at $GOBIND after install." >&2
    exit 1
fi
echo "    gomobile  : $GOMOBILE"
echo "    gobind    : $GOBIND"

# ---- 5. gomobile init -------------------------------------------------------
# Downloads the Go standard library pre-built for Android targets.
# Skipped automatically if already done (cached in GOPATH).
echo "==> Running gomobile init ..."
cd "$REPO_ROOT"
"$GOMOBILE" init

# ---- 6. Build the AAR -------------------------------------------------------
mkdir -p "$(dirname "$OUTPUT")"
echo "==> Running gomobile bind ..."
"$GOMOBILE" bind \
    -target=android \
    -androidapi 21 \
    -o "$OUTPUT" \
    "$MOBILE_PKG"

echo ""
echo "✓  AAR written to: $OUTPUT"
echo "   Regenerate whenever mobile/vitallink/vitallink.go changes."
