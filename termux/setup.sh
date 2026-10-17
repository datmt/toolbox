#!/data/data/com.termux/files/usr/bin/bash
# Termux bootstrap: Nerd Font, neovim, ssh.
#
#   curl -fsSL https://raw.githubusercontent.com/datmt/toolbox/main/termux/setup.sh | bash
#
# Env overrides:
#   GITHUB_USER=name      import ssh public keys from github.com/name.keys
#   NVIM_CONFIG_REPO=url  clone this repo as nvim config instead of the built-in one
#   SKIP_SSH=1            skip ssh setup
#   SKIP_NVIM=1           skip neovim setup
#   SKIP_FONT=1           skip font install
#   SKIP_UPGRADE=1        skip pkg upgrade (faster)

set -uo pipefail

RAW_BASE="${RAW_BASE:-https://raw.githubusercontent.com/datmt/toolbox/main/termux}"
FONT_NAME="FiraCodeNerdFontMono-Regular.ttf"
FONT_ZIP_URL="https://github.com/ryanoasis/nerd-fonts/releases/download/v3.5.1/FiraCode.zip"

GITHUB_USER="${GITHUB_USER:-}"
NVIM_CONFIG_REPO="${NVIM_CONFIG_REPO:-}"

FAILED=()
DONE=()
TMP_DIRS=()

cleanup() {
  local d
  for d in "${TMP_DIRS[@]:-}"; do
    [ -n "$d" ] && rm -rf "$d"
  done
}
trap cleanup EXIT
trap 'echo; echo "Interrupted."; exit 130' INT TERM

log()  { printf '\n==> %s\n' "$*"; }
warn() { printf '  ! %s\n' "$*" >&2; }
ok()   { printf '  ok: %s\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }
ts()   { date +%Y%m%d%H%M%S; }

mktmp() {
  local d
  d=$(mktemp -d) || return 1
  TMP_DIRS+=("$d")
  printf '%s' "$d"
}

# step "label" function: run function, record result, never abort the script.
step() {
  local label=$1 fn=$2
  log "$label"
  if "$fn"; then
    DONE+=("$label")
  else
    warn "$label failed"
    FAILED+=("$label")
  fi
}

# Read an answer from the terminal even when stdin is the curl pipe.
ask() {
  local prompt=$1 answer=""
  if [ -r /dev/tty ] && [ -w /dev/tty ]; then
    printf '%s' "$prompt" > /dev/tty
    read -r answer < /dev/tty || answer=""
  fi
  printf '%s' "$answer"
}

backup() {
  local f=$1
  if [ -e "$f" ]; then
    cp -a "$f" "$f.bak.$(ts)" && ok "backed up $f"
  fi
}

preflight() {
  if [ -z "${TERMUX_VERSION:-}" ] && [[ "${PREFIX:-}" != *com.termux* ]]; then
    echo "This script is for Termux on Android." >&2
    exit 1
  fi
  if ! have curl; then
    echo "curl missing, installing..."
    pkg install -y curl || { echo "cannot install curl" >&2; exit 1; }
  fi
  if ! curl -fsSI --max-time 10 https://github.com >/dev/null 2>&1; then
    echo "No network (cannot reach github.com)." >&2
    exit 1
  fi
}

# ---------------------------------------------------------------- packages

pkg_opts=(-y -o Dpkg::Options::=--force-confold)

install_packages() {
  local pkgs=(git curl unzip tmux)
  [ "${SKIP_NVIM:-0}" != 1 ] && pkgs+=(neovim ripgrep fd clang make nodejs termux-api)
  [ "${SKIP_SSH:-0}" != 1 ]  && pkgs+=(openssh)

  pkg update "${pkg_opts[@]}" || warn "pkg update failed (mirror down? try: termux-change-repo)"
  if [ "${SKIP_UPGRADE:-0}" != 1 ]; then
    pkg upgrade "${pkg_opts[@]}" || warn "pkg upgrade failed, continuing"
  fi

  local p missing=()
  for p in "${pkgs[@]}"; do
    pkg install "${pkg_opts[@]}" "$p" >/dev/null 2>&1 || missing+=("$p")
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    warn "could not install: ${missing[*]}"
    return 1
  fi
}

# -------------------------------------------------------------------- font

install_font() {
  mkdir -p "$HOME/.termux" || return 1
  local dest="$HOME/.termux/font.ttf" tmp
  tmp=$(mktmp) || return 1

  # Single file from the repo (2.7MB) first, release zip (28MB) as fallback.
  if ! curl -fsSL --retry 2 "$RAW_BASE/fonts/$FONT_NAME" -o "$tmp/font.ttf" \
     || [ ! -s "$tmp/font.ttf" ]; then
    warn "repo font unavailable, trying nerd-fonts release zip"
    have unzip || { warn "unzip missing"; return 1; }
    curl -fsSL --retry 2 "$FONT_ZIP_URL" -o "$tmp/f.zip" || { warn "zip download failed"; return 1; }
    unzip -joq "$tmp/f.zip" "$FONT_NAME" -d "$tmp" || { warn "font not found in zip"; return 1; }
    mv "$tmp/$FONT_NAME" "$tmp/font.ttf" || return 1
  fi

  backup "$dest"
  cp "$tmp/font.ttf" "$dest" || return 1
  ok "font installed to $dest"
}

termux_props() {
  local props="$HOME/.termux/termux.properties"
  mkdir -p "$HOME/.termux" || return 1
  touch "$props" || return 1
  if ! grep -Eq '^[[:space:]]*extra-keys' "$props"; then
    cat >> "$props" <<'EOF'
extra-keys = [['ESC','|','/','HOME','UP','END','PGUP'],['TAB','CTRL','ALT','LEFT','DOWN','RIGHT','PGDN']]
EOF
    ok "extra keys row added"
  else
    ok "extra-keys already set, left alone"
  fi
}

# -------------------------------------------------------------------- nvim

write_nvim_config() {
  mkdir -p "$HOME/.config/nvim" || return 1
  cat > "$HOME/.config/nvim/init.lua" <<'EOF'
-- Minimal defaults written by termux/setup.sh
local o = vim.opt

-- look
o.number = true
o.relativenumber = false
o.cursorline = true
o.termguicolors = true
o.signcolumn = "yes"
o.scrolloff = 5
o.wrap = false
vim.cmd("syntax enable")
vim.cmd("filetype plugin indent on")
pcall(vim.cmd, "colorscheme habamax")

-- tabs: 2 spaces, no tab characters
o.expandtab = true
o.tabstop = 2
o.shiftwidth = 2
o.softtabstop = 2
o.smartindent = true

-- search
o.ignorecase = true
o.smartcase = true
o.hlsearch = true
o.incsearch = true

-- misc
o.mouse = "a"
o.splitright = true
o.splitbelow = true
o.undofile = true
o.updatetime = 300
vim.g.mapleader = " "

-- clear search highlight
vim.keymap.set("n", "<Esc>", "<cmd>nohlsearch<CR>")

-- paste: use the Android clipboard via Termux:API when available
if vim.fn.executable("termux-clipboard-set") == 1 then
  vim.g.clipboard = {
    name = "termux",
    copy = {
      ["+"] = "termux-clipboard-set",
      ["*"] = "termux-clipboard-set",
    },
    paste = {
      ["+"] = "termux-clipboard-get",
      ["*"] = "termux-clipboard-get",
    },
    cache_enabled = 0,
  }
  o.clipboard = "unnamedplus"
end

-- insert mode paste from the terminal (long-press paste) works via bracketed
-- paste; this adds an explicit fallback toggle
vim.keymap.set("n", "<leader>p", "<cmd>set paste!<CR>", { desc = "toggle paste mode" })
EOF
}

setup_nvim() {
  have nvim || { warn "nvim not installed"; return 1; }
  local cfg="$HOME/.config/nvim"

  if [ -n "$NVIM_CONFIG_REPO" ]; then
    if [ -d "$cfg" ]; then
      mv "$cfg" "$cfg.bak.$(ts)" || return 1
      ok "old config moved to $cfg.bak.*"
    fi
    git clone --depth 1 "$NVIM_CONFIG_REPO" "$cfg" || return 1
    ok "cloned $NVIM_CONFIG_REPO"
    return 0
  fi

  if [ -f "$cfg/init.lua" ] || [ -f "$cfg/init.vim" ]; then
    ok "existing nvim config found, left alone"
    return 0
  fi

  write_nvim_config || return 1
  ok "wrote $cfg/init.lua"
}

# --------------------------------------------------------------------- ssh

setup_ssh() {
  have ssh-keygen || { warn "openssh not installed"; return 1; }
  local ssh_dir="$HOME/.ssh" auth="$HOME/.ssh/authorized_keys"
  mkdir -p "$ssh_dir" && chmod 700 "$ssh_dir" || return 1

  if [ ! -f "$ssh_dir/id_ed25519" ]; then
    ssh-keygen -t ed25519 -N "" -f "$ssh_dir/id_ed25519" -C "$(whoami)@termux" >/dev/null \
      || return 1
    ok "generated $ssh_dir/id_ed25519"
  fi

  local user="$GITHUB_USER"
  [ -z "$user" ] && user=$(ask "GitHub username to import ssh keys from (blank to skip): ")

  if [ -n "$user" ]; then
    local keys
    keys=$(curl -fsSL --max-time 15 "https://github.com/$user.keys") \
      && [ -n "$keys" ] \
      || { warn "no keys fetched for '$user'"; keys=""; }
    if [ -n "$keys" ]; then
      touch "$auth" && chmod 600 "$auth" || return 1
      printf '%s\n' "$keys" >> "$auth"
      sort -u "$auth" -o "$auth"
      ok "imported keys for $user"
    fi
  fi

  if [ ! -s "$auth" ]; then
    warn "no authorized_keys; set a password to log in: run 'passwd'"
  fi

  have termux-wake-lock && termux-wake-lock 2>/dev/null
  if pgrep -x sshd >/dev/null 2>&1; then
    ok "sshd already running"
  else
    sshd || { warn "sshd failed to start"; return 1; }
    ok "sshd started"
  fi

  mkdir -p "$HOME/.termux/boot" 2>/dev/null && cat > "$HOME/.termux/boot/sshd.sh" <<'EOF'
#!/data/data/com.termux/files/usr/bin/sh
termux-wake-lock
sshd
EOF
  chmod +x "$HOME/.termux/boot/sshd.sh" 2>/dev/null

  local ip=""
  if have ip; then
    ip=$(ip -4 addr show wlan0 2>/dev/null | awk '/inet /{sub("/.*","",$2); print $2; exit}')
  fi
  SSH_HINT="ssh -p 8022 $(whoami)@${ip:-<phone-ip>}"
}

# -------------------------------------------------------------------- main

SSH_HINT=""

preflight

step "Packages" install_packages

if [ "${SKIP_FONT:-0}" != 1 ]; then
  step "Nerd Font" install_font
  step "Extra keys row" termux_props
fi

[ "${SKIP_NVIM:-0}" != 1 ] && step "Neovim config" setup_nvim
[ "${SKIP_SSH:-0}"  != 1 ] && step "SSH" setup_ssh

have termux-reload-settings && termux-reload-settings 2>/dev/null

printf '\n==================== summary ====================\n'
for s in "${DONE[@]:-}";   do [ -n "$s" ] && printf '  [ok]     %s\n' "$s"; done
for s in "${FAILED[@]:-}"; do [ -n "$s" ] && printf '  [FAILED] %s\n' "$s"; done
[ -n "$SSH_HINT" ] && printf '\nConnect from another machine:\n  %s\n' "$SSH_HINT"
cat <<'EOF'

Manual follow-ups:
  - Restart Termux to apply the font.
  - Clipboard in nvim needs the Termux:API app (F-Droid).
  - ssh autostart on boot needs the Termux:Boot app (F-Droid), opened once.
EOF

[ "${#FAILED[@]}" -eq 0 ]
