#!/bin/sh
set -u

PKG_URL="${1:-}"
SHA_URL="${2:-}"
TARGET_VERSION="${3:-}"
ASSET_NAME="${4:-}"
STATE_FILE="${5:-/share/Public/QnapAssistant/update-state}"
DATA_DIR="/share/Public/QnapAssistant"
WORK_DIR="$DATA_DIR/update-cache"
LOG_FILE="${QNAPASSISTANT_UPDATE_LOG:-$DATA_DIR/update.log}"
QPKG_CLI="/sbin/qpkg_cli"
GETCFG="/sbin/getcfg"
QPKG_CONF="/etc/config/qpkg.conf"
INSTALL_STARTED=0

sanitize_message() {
    printf '%s' "$1" | tr '\r\n=' '   '
}

write_state() {
    STATUS="$1"
    MESSAGE="$(sanitize_message "$2")"
    mkdir -p "$(dirname "$STATE_FILE")"
    TMP_STATE="${STATE_FILE}.tmp.$$"
    {
        printf 'status=%s\n' "$STATUS"
        printf 'target_version=%s\n' "$TARGET_VERSION"
        printf 'message=%s\n' "$MESSAGE"
        printf 'updated_at=%s\n' "$(date +%s)"
    } > "$TMP_STATE" && mv "$TMP_STATE" "$STATE_FILE"
}

installed_version() {
    "$GETCFG" QnapAssistant Version -f "$QPKG_CONF" 2>/dev/null || true
}

restart_existing_package() {
    "$QPKG_CLI" --enable QnapAssistant >/dev/null 2>&1 || true
    "$QPKG_CLI" --start QnapAssistant >/dev/null 2>&1 || true
}

fail() {
    MSG="$1"
    echo "Self-update failed: $MSG" >&2
    write_state failed "$MSG"
    if [ "$INSTALL_STARTED" -eq 1 ]; then
        restart_existing_package
    fi
    exit 1
}

download_file() {
    URL="$1"
    DEST="$2"
    PART="${DEST}.part"
    rm -f "$PART"
    if command -v curl >/dev/null 2>&1; then
        curl -fL --retry 4 --retry-delay 3 --connect-timeout 30 -o "$PART" "$URL" || return 1
    elif command -v wget >/dev/null 2>&1; then
        wget --tries=4 --timeout=30 -O "$PART" "$URL" || return 1
    else
        return 127
    fi
    mv "$PART" "$DEST"
}

file_sha256() {
    FILE="$1"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$FILE" | awk '{print $1}'
        return
    fi
    if command -v openssl >/dev/null 2>&1; then
        openssl dgst -sha256 "$FILE" | awk '{print $NF}'
        return
    fi
    return 127
}

[ -n "$PKG_URL" ] || fail "missing QPKG URL"
[ -n "$SHA_URL" ] || fail "missing checksum URL"
[ -n "$TARGET_VERSION" ] || fail "missing target version"
[ -n "$ASSET_NAME" ] || fail "missing QPKG asset name"
[ -x "$QPKG_CLI" ] || fail "qpkg_cli is unavailable"
[ -x "$GETCFG" ] || fail "getcfg is unavailable"

case "$ASSET_NAME" in
    */*|*\\*|''|*.qpkg) ;;
    *) fail "unexpected QPKG asset name" ;;
esac
case "$ASSET_NAME" in
    */*|*\\*) fail "invalid QPKG asset path" ;;
esac

mkdir -p "$WORK_DIR" || fail "cannot create update cache"
chmod 700 "$WORK_DIR" 2>/dev/null || true
PKG_PATH="$WORK_DIR/$ASSET_NAME"
SHA_PATH="$PKG_PATH.sha256"

write_state downloading "Downloading QnapAssistant $TARGET_VERSION"
echo "Downloading $PKG_URL"
download_file "$PKG_URL" "$PKG_PATH" || fail "QPKG download failed"
download_file "$SHA_URL" "$SHA_PATH" || fail "checksum download failed"

write_state verifying "Verifying SHA-256"
EXPECTED="$(awk 'NR==1 {print $1}' "$SHA_PATH" | tr 'A-F' 'a-f')"
printf '%s\n' "$EXPECTED" | grep -Eq '^[0-9a-f]{64}$' || fail "invalid checksum file"
ACTUAL="$(file_sha256 "$PKG_PATH" | tr 'A-F' 'a-f')" || fail "no SHA-256 implementation is available"
[ "$ACTUAL" = "$EXPECTED" ] || fail "SHA-256 mismatch"
echo "SHA-256 verified: $ACTUAL"

write_state installing "Installing QnapAssistant $TARGET_VERSION"
INSTALL_STARTED=1

# QTS 5.x accepts a local QPKG with -m. -A 1/-q/-K are supported on current
# QTS builds and keep the install non-interactive. If that variant is rejected
# before installation, retry the portable local-install form once.
if "$QPKG_CLI" -m "$PKG_PATH" -A 1 -q -K; then
    INSTALL_RC=0
else
    INSTALL_RC=$?
    NOW="$(installed_version)"
    if [ "$NOW" != "$TARGET_VERSION" ]; then
        echo "qpkg_cli extended flags returned $INSTALL_RC; retrying basic local install"
        "$QPKG_CLI" -m "$PKG_PATH" || fail "QPKG installation failed"
    fi
fi

NOW="$(installed_version)"
[ "$NOW" = "$TARGET_VERSION" ] || fail "installed version is '$NOW', expected '$TARGET_VERSION'"

write_state restarting "Restarting QnapAssistant $TARGET_VERSION"
restart_existing_package
sleep 2

write_state complete "Updated to QnapAssistant $TARGET_VERSION"
rm -f "$SHA_PATH" "$PKG_PATH" 2>/dev/null || true
echo "QnapAssistant self-update completed: $TARGET_VERSION"
exit 0
