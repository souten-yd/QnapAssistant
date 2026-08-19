#!/bin/sh
set -u

QPKG_NAME="QnapAssistant"
QPKG_CONF="/etc/config/qpkg.conf"
QPKG_DIR=$(/sbin/getcfg "$QPKG_NAME" Install_Path -f "$QPKG_CONF")
DATA_DIR="/share/Public/QnapAssistant"
PID_FILE="$DATA_DIR/qnapassistant.pid"
CONFIG_FILE="$DATA_DIR/config.env"
DEFAULT_CONFIG="$QPKG_DIR/config.env.default"
LOG_FILE="$DATA_DIR/admin.log"
SERVER="$QPKG_DIR/bin/qnap-assistant"

pid_matches_server() {
    CANDIDATE="$1"
    [ -n "$CANDIDATE" ] || return 1
    kill -0 "$CANDIDATE" 2>/dev/null || return 1
    ps 2>/dev/null | awk -v pid="$CANDIDATE" -v server="$SERVER" '
        $1 == pid && index($0, server) > 0 { found=1 }
        END { exit(found ? 0 : 1) }
    '
}

find_running_pid() {
    ps 2>/dev/null | awk -v server="$SERVER" '
        index($0, server) > 0 { print $1; exit }
    '
}

is_running() {
    PID=""
    if [ -f "$PID_FILE" ]; then
        PID=$(cat "$PID_FILE" 2>/dev/null || true)
        if pid_matches_server "$PID"; then
            return 0
        fi
        rm -f "$PID_FILE" 2>/dev/null || true
    fi

    PID=$(find_running_pid)
    if pid_matches_server "$PID"; then
        mkdir -p "$DATA_DIR" 2>/dev/null || true
        echo "$PID" > "$PID_FILE" 2>/dev/null || true
        return 0
    fi
    PID=""
    return 1
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

    prepare_data
    if is_running; then
        echo "$QPKG_NAME is already running (PID $PID)."
        return 0
    fi
    if [ ! -x "$SERVER" ]; then
        echo "Management server missing: $SERVER" >&2
        return 1
    fi

    cd "$QPKG_DIR" || return 1

    # QTS/BusyBox installations do not always provide `nohup`. The Go daemon
    # ignores SIGHUP itself, and all standard streams are detached here.
    QPKG_DIR="$QPKG_DIR" QNAP_ASSISTANT_CONFIG="$CONFIG_FILE" \
        "$SERVER" </dev/null >>"$LOG_FILE" 2>&1 &
    PID=$!
    if ! echo "$PID" > "$PID_FILE" 2>/dev/null; then
        echo "Warning: could not write PID file $PID_FILE; process discovery fallback will be used." >&2
    fi
    sleep 1
    if ! pid_matches_server "$PID"; then
        rm -f "$PID_FILE" 2>/dev/null || true
        echo "$QPKG_NAME failed to launch. See $LOG_FILE" >&2
        return 1
    fi
    echo "$QPKG_NAME management API started with PID $PID."
    echo "The LLM stays unloaded until an OpenAI API request arrives."
}

stop_service() {
    if ! is_running; then
        rm -f "$PID_FILE" 2>/dev/null || true
        echo "$QPKG_NAME is not running."
        return 0
    fi

    kill "$PID" 2>/dev/null || true
    COUNT=0
    while pid_matches_server "$PID" && [ "$COUNT" -lt 20 ]; do
        sleep 1
        COUNT=$((COUNT + 1))
    done
    if pid_matches_server "$PID"; then
        kill -9 "$PID" 2>/dev/null || true
    fi
    rm -f "$PID_FILE" 2>/dev/null || true
    echo "$QPKG_NAME stopped."
}

case "${1:-}" in
    start) start_service ;;
    stop) stop_service ;;
    restart) stop_service; start_service ;;
    status)
        if is_running; then
            echo "$QPKG_NAME manager is running (PID $PID)."
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
