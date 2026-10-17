#!/usr/bin/env bash
# Records the README / website demos.
#
#   demo/record.sh            # both
#   demo/record.sh cli|tui    # one
#
# Drives a real sigmoidh run: asciinema records inside a detached tmux pane
# (so no tmux chrome is captured) and keystrokes are typed with send-keys.
# Outputs docs/media/<name>.gif (README) and website/public/media/<name>.{mp4,jpg}.
# Needs: tmux, asciinema, agg, ffmpeg, bwrap, and a configured provider in .env.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
MODEL=${MODEL:-liquid/lfm-2.5-2.6b:free}
COLS=100 ROWS=30
BIN=$ROOT/sigmoidh
OUT=$ROOT/demo/out
mkdir -p "$OUT" "$ROOT/docs/media" "$ROOT/website/public/media"

(cd "$ROOT" && go build -o sigmoidh ./cmd/sigmoidh)

type_line() { # human-paced typing, then Enter
  local s=$1
  for ((i = 0; i < ${#s}; i++)); do
    tmux send-keys -t "$sess" -l "${s:i:1}"
    sleep 0.05
  done
  sleep 0.5
  tmux send-keys -t "$sess" Enter
}

wait_idle() { # until the agent is ready for input again, max 4min
  # "unchanged for a few seconds" is not enough: a model can think silently
  # for longer. Ready = the TUI footer says "idle" / the CLI prompt is back,
  # and the screen has settled.
  local prev="" cur same=0 pane last
  for ((t = 0; t < 480; t++)); do
    pane=$(tmux capture-pane -p -t "$sess")
    cur=$(printf '%s' "$pane" | md5sum)
    if [[ $cur == "$prev" ]]; then same=$((same + 1)); else same=0; fi
    prev=$cur
    last=$(printf '%s\n' "$pane" | grep -v '^[[:space:]]*$' | tail -1)
    if ((same >= 4)) && { grep -q '○ idle' <<<"$pane" || [[ $last == '❯'* ]]; }; then
      return
    fi
    sleep 0.5
  done
}

record() { # record <name> <sigmoidh flags> <prompt>...
  local name=$1 flags=$2
  shift 2
  sess=sigmoidh-rec-$name
  local work=$ROOT/demo/work
  rm -rf "$work" && cp -r "$ROOT/demo/fixture" "$work"

  tmux kill-session -t "$sess" 2>/dev/null || true
  # Run inside bubblewrap: / is read-only and /tmp private, so the agent can
  # only write to its throwaway project copy; /home is a tmpfs where the
  # project appears as /home/demo/fib (no real home path on screen, no access
  # to it). Only the API keys (.env) are bound in, read-only; session files
  # sigmoidh writes vanish with the sandbox.
  # (bound as the global env file, never into the project where the model
  # would list it)
  local env_file=$HOME/.sigmoidh/.env env_args=""
  [[ -f $env_file ]] || env_file=$ROOT/.env
  [[ -f $env_file ]] && env_args="--ro-bind $env_file /home/demo/.sigmoidh/.env"
  local run="bwrap --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp --tmpfs /home --dir /home/demo/.sigmoidh \
    --ro-bind $BIN /home/demo/.local/bin/sigmoidh --bind $work /home/demo/fib $env_args \
    --setenv HOME /home/demo --setenv PWD /home/demo/fib --chdir /home/demo/fib \
    /home/demo/.local/bin/sigmoidh -model $MODEL $flags"

  tmux new-session -d -s "$sess" -x $COLS -y $ROWS -c "$work" \
    "asciinema rec --overwrite -q --idle-time-limit 1.5 --window-size ${COLS}x${ROWS} \
       -c '$run' '$OUT/$name.cast'"
  sleep 3
  local p
  for p in "$@"; do
    type_line "$p"
    sleep 2
    wait_idle
    sleep 1.5
  done
  # end on the final answer: killing the pane stops asciinema (the cast is
  # streamed to disk), where /exit would end the TUI clip on a blank screen
  sleep 1
  tmux kill-session -t "$sess" 2>/dev/null || true
  # asciinema outlives its pane (it ignores the hangup); the cast is already
  # on disk, so stop it hard rather than leak a recorder per take
  sleep 1
  pkill -9 -f -- "$OUT/$name.cast" 2>/dev/null || true

  agg -q --theme github-dark --font-size 16 --idle-time-limit 1.5 --last-frame-duration 4 \
    "$OUT/$name.cast" "$ROOT/docs/media/$name.gif"
  ffmpeg -loglevel error -y -i "$ROOT/docs/media/$name.gif" \
    -movflags +faststart -pix_fmt yuv420p -vf "fps=20,scale=trunc(iw/2)*2:trunc(ih/2)*2" -fps_mode cfr \
    -c:v libx264 -crf 24 "$ROOT/website/public/media/$name.mp4"
  # poster: the final frame, shown before the video loads
  ffmpeg -loglevel error -y -sseof -0.5 -i "$ROOT/website/public/media/$name.mp4" \
    -frames:v 1 -update 1 -q:v 4 "$ROOT/website/public/media/$name.jpg"
  echo "recorded $name"
}

what=${1:-all}
if [[ $what == all || $what == cli ]]; then
  record cli "" "what does main.go do?" "add a doc comment above fib explaining it"
fi
if [[ $what == all || $what == tui ]]; then
  record tui "-tui" "explain main.go in two sentences" "make fib iterative instead of recursive"
fi
