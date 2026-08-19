#!/bin/sh
set -u

QPKG_NAME="QnapAssistant"
QPKG_CONF="/etc/config/qpkg.conf"
QPKG_DIR=$(/sbin/getcfg "$QPKG_NAME" Install_Path -f "$QPKG_CONF")
PID_FILE="$QPKG_DIR/qnapassistant.pid"
DATA_DIR="/share/Public/QnapAssistant"
CONFIG_FILE="$DATA_DIR/config.env"
DEFAULT_CONFIG="$QPKG_DIR/config.env.default"
LOG_FILE="$DATA_DIR/admin.log"
SERVER="$QPKG_DIR/bin/qnap-assistant"

is_running() {
    [ -f "$PID_FILE" ] || return 1
    PID=$(cat "$PID_FILE" 2>/dev/null) || return 1
    [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null
}

prepare_data() {
    mkdir -p "$DATA_DIR"
    if [ ! -f "$CONFIG_FILE" ]; then
        cp "$DEFAULT_CONFIG" "$CONFIG_FILE"
        chmod 0644 "$CONFIG_FILE"
    fi
    touch "$LOG_FILE" "$DATA_DIR/llama-server.log"
}

start_service() {
    ENABLED=$(/sbin/getcfg "$QPKG_NAME" Enable -u -d FALSE -f "$QPKG_CONF")
    if [ "$ENABLED" != "TRUE" ]; then
        echo "$QPKG_NAME is disabled."
        exit 1
    fi
    if is_running; then
        echo "$QPKG_NAME is already running."
        return 0
    fi
    if [ ! -x "$SERVER" ]; then
        echo "Management server missing: $SERVER" >&2
        return 1
    fi

    prepare_data
    cd "$QPKG_DIR" || return 1

    # QTS/BusyBox installations do not always provide `nohup`.  This service
    # already redirects all standard streams, so a plain non-interactive
    # background launch is sufficient and avoids an optional coreutils
    # dependency on the NAS.
    QPKG_DIR="$QPKG_DIR" QNAP_ASSISTANT_CONFIG="$CONFIG_FILE" \
        "$SERVER" </dev/null >>"$LOG_FILE" 2>&1 &
    PID=$!
    echo "$PID" > "$PID_FILE"
    sleep 1
    if ! kill -0 "$PID" 2>/dev/null; then
        rm -f "$PID_FILE"
        echo "$QPKG_NAME failed to launch. See $LOG_FILE" >&2
        return 1
    fi
    echo "$QPKG_NAME management API started with PID $PID."
    echo "The LLM stays unloaded until an OpenAI API request arrives."
}

stop_service() {
    if ! is_running; then
        rm -f "$PID_FILE"
        echo "$QPKG_NAME is not running."
        return 0
    fi
    PID=$(cat "$PID_FILE")
    kill "$PID" 2>/dev/null || true
    COUNT=0
    while kill -0 "$PID" 2>/dev/null && [ "$COUNT" -lt 20 ]; do
        sleep 1
        COUNT=$((COUNT + 1))
    done
    if kill -0 "$PID" 2>/dev/null; then
        kill -9 "$PID" 2>/dev/null || true
    fi
    rm -f "$PID_FILE"
    echo "$QPKG_NAME stopped."
}

case "${1:-}" in
    start) start_service ;;
    stop) stop_service ;;
    restart) stop_service; start_service ;;
    status)
        if is_running; then
            echo "$QPKG_NAME manager is running (PID $(cat "$PID_FILE"))."
            exit 0
        fi
        echo "$QPKG_NAME is stopped."
        exit 1
        ;;
    *)
        echo "Usage: $0 {start|stop|restart|status}"
        exit 1
        ;;
esac
