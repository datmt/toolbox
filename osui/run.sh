#!/usr/bin/env bash
# Build (if needed) and run osui locally.
#
#   ./run.sh              run against the real opensnitchd (socket /tmp/osui.sock).
#                         Stops opensnitch-ui if it is running; if osui is already
#                         running, just prints its URL.
#   ./run.sh --keep-ui    refuse to start while opensnitch-ui is running
#   ./run.sh --restart    restart osui even if it is up to date (it also restarts
#                         automatically when the binary was rebuilt)
#   ./run.sh demo         run with a fake daemon that fires sample prompts;
#                         never touches the real firewall
#
# Env: OSUI_LISTEN (default 127.0.0.1:8899, demo: 127.0.0.1:8898)
set -euo pipefail
cd "$(dirname "$(readlink -f "$0")")"

mode=real
keep_ui=0
restart=0
for a in "$@"; do
  case "$a" in
    demo) mode=demo ;;
    --keep-ui) keep_ui=1 ;;
    --restart) restart=1 ;;
    -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
    *) echo "unknown argument: $a (try --help)" >&2; exit 2 ;;
  esac
done

command -v go >/dev/null || { echo "go is not installed" >&2; exit 1; }

# Rebuild when the binary is missing or any source file is newer.
newer=$(find . \( -name '*.go' -o -name '*.html' -o -name '*.js' -o -name '*.css' -o -name go.mod \) -newer osui -print -quit 2>/dev/null || true)
if [[ ! -x osui || -n $newer ]]; then
  echo "building osui…"
  go build -o osui .
fi

pids=()
cleanup() { for p in "${pids[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT INT TERM

if [[ $mode == demo ]]; then
  sock=unix:///tmp/osui-demo.sock
  listen=${OSUI_LISTEN:-127.0.0.1:8898}
  go build -o fakedaemon ./cmd/fakedaemon
  ./osui --socket "$sock" --listen "$listen" --notify=false &
  pids+=($!)
  sleep 1
  ./fakedaemon --addr "$sock" --every 6s >/dev/null &
  pids+=($!)
  echo "demo mode: fake daemon sends a prompt every 6s. Ctrl-C to stop."
  wait "${pids[0]}"
  exit 0
fi

sock=unix:///tmp/osui.sock
listen=${OSUI_LISTEN:-127.0.0.1:8899}

cfg=${XDG_CONFIG_HOME:-$HOME/.config}/osui/token
url() { echo "http://$listen/?token=$(cat "$cfg" 2>/dev/null)"; }

# Already running with the current binary: just show where it is. If the binary
# was rebuilt since that process started (or --restart), restart it.
if running=$(pgrep -f '^\./osui --socket unix:///tmp/osui\.sock'); then
  stale=0
  [[ $(stat -c %Y osui) -gt $(stat -c %Y "/proc/$running") ]] && stale=1
  if (( stale || restart )); then
    if (( stale )); then echo "restarting osui (pid $running): it is running an older build."; else echo "restarting osui (pid $running)."; fi
    kill $running
    for _ in 1 2 3 4 5 6 7 8 9 10; do kill -0 "$running" 2>/dev/null || break; sleep 0.3; done
  else
    echo "osui is already running (pid $running)."
    echo
    echo "Open the UI:  $(url)"
    exit 0
  fi
fi

ui_pids=$(pgrep -f '^/usr/bin/python.*opensnitch-ui' || true)
if [[ -n $ui_pids ]]; then
  if (( keep_ui )); then
    echo "opensnitch-ui is running (pid $ui_pids) and owns $sock." >&2
    echo "Rerun without --keep-ui to stop it automatically." >&2
    exit 1
  fi
  echo "stopping opensnitch-ui (pid $ui_pids)."
  echo "NOTE: until osui connects, unmatched connections follow DefaultAction in /etc/opensnitchd/default-config.json."
  kill $ui_pids
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    pgrep -f '^/usr/bin/python.*opensnitch-ui' >/dev/null || break
    sleep 0.5
  done
fi

exec ./osui --socket "$sock" --listen "$listen"
