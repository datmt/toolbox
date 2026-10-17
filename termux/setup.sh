#!/data/data/com.termux/files/usr/bin/bash
# Termux bootstrap: Nerd Font, neovim, ssh, shell prompt.
#
#   curl -fsSL https://raw.githubusercontent.com/datmt/toolbox/main/termux/setup.sh | bash
#
# Safe to re-run: every stage checks what is already in place and only adds
# what is missing.
#
# Env overrides:
#   SHELL_CHOICE=bash|zsh|fish  shell to set up (default bash, login shell untouched for bash)
#   PROMPT=starship|none        prompt (default starship)
#   GITHUB_USER=name            import ssh public keys from github.com/name.keys
#   NVIM_CONFIG_REPO=url        clone this repo as nvim config instead of the built-in one
#   SKIP_SSH=1 SKIP_NVIM=1 SKIP_FONT=1 SKIP_SHELL=1   skip that stage
#   SKIP_UPGRADE=1              skip pkg upgrade
#   FORCE_FONT=1                replace an existing ~/.termux/font.ttf

set -uo pipefail

RAW_BASE="${RAW_BASE:-https://raw.githubusercontent.com/datmt/toolbox/main/termux}"
FONT_NAME="FiraCodeNerdFontMono-Regular.ttf"
FONT_ZIP_URL="https://github.com/ryanoasis/nerd-fonts/releases/download/v3.5.1/FiraCode.zip"

GITHUB_USER="${GITHUB_USER:-}"
NVIM_CONFIG_REPO="${NVIM_CONFIG_REPO:-}"
SHELL_CHOICE="${SHELL_CHOICE:-bash}"
PROMPT="${PROMPT:-starship}"

FAILED=()
DONE=()
TMP_DIRS=()
SSH_HINT=""

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
skip() { printf '  --  %s (already done)\n' "$*"; }
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

# add_block FILE MARKER CONTENT: append a marked block once. Backs up FILE
# before the first change.
add_block() {
  local file=$1 marker=$2 content=$3
  mkdir -p "$(dirname "$file")" || return 1
  if [ -f "$file" ] && grep -qF "# >>> $marker >>>" "$file"; then
    skip "$marker in $file"
    return 0
  fi
  backup "$file"
  {
    printf '\n# >>> %s >>>\n' "$marker"
    printf '%s\n' "$content"
    printf '# <<< %s <<<\n' "$marker"
  } >> "$file" || return 1
  ok "added $marker to $file"
}

pkg_installed() { dpkg -s "$1" >/dev/null 2>&1; }

preflight() {
  if [ -z "${TERMUX_VERSION:-}" ] && [[ "${PREFIX:-}" != *com.termux* ]]; then
    echo "This script is for Termux on Android." >&2
    exit 1
  fi
  case "$SHELL_CHOICE" in
    bash|zsh|fish) ;;
    *) echo "SHELL_CHOICE must be bash, zsh or fish (got '$SHELL_CHOICE')." >&2; exit 1 ;;
  esac
  case "$PROMPT" in
    starship|none) ;;
    *) echo "PROMPT must be starship or none (got '$PROMPT')." >&2; exit 1 ;;
  esac
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

wanted_packages() {
  local pkgs=(git curl unzip tmux)
  if [ "${SKIP_NVIM:-0}" != 1 ]; then
    pkgs+=(neovim ripgrep fd clang make nodejs termux-api)
  fi
  [ "${SKIP_SSH:-0}" != 1 ] && pkgs+=(openssh)
  if [ "${SKIP_SHELL:-0}" != 1 ]; then
    [ "$PROMPT" = starship ] && pkgs+=(starship)
    case "$SHELL_CHOICE" in
      zsh)  pkgs+=(zsh zsh-autosuggestions zsh-syntax-highlighting) ;;
      fish) pkgs+=(fish) ;;
    esac
  fi
  printf '%s\n' "${pkgs[@]}"
}

install_packages() {
  local p missing=()
  while IFS= read -r p; do
    pkg_installed "$p" || missing+=("$p")
  done < <(wanted_packages)

  if [ "${#missing[@]}" -eq 0 ]; then
    skip "all packages installed"
    return 0
  fi

  echo "  missing: ${missing[*]}"
  pkg update "${pkg_opts[@]}" || warn "pkg update failed (mirror down? try: termux-change-repo)"
  if [ "${SKIP_UPGRADE:-0}" != 1 ]; then
    pkg upgrade "${pkg_opts[@]}" || warn "pkg upgrade failed, continuing"
  fi

  local failed=()
  for p in "${missing[@]}"; do
    if pkg install "${pkg_opts[@]}" "$p" >/dev/null 2>&1; then
      ok "installed $p"
    else
      failed+=("$p")
    fi
  done
  if [ "${#failed[@]}" -gt 0 ]; then
    warn "could not install: ${failed[*]}"
    return 1
  fi
}

# -------------------------------------------------------------------- font

install_font() {
  mkdir -p "$HOME/.termux" || return 1
  local dest="$HOME/.termux/font.ttf" tmp

  if [ -s "$dest" ] && [ "${FORCE_FONT:-0}" != 1 ]; then
    skip "font present at $dest (FORCE_FONT=1 to replace)"
    return 0
  fi

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
  if grep -Eq '^[[:space:]]*extra-keys' "$props"; then
    skip "extra-keys already set"
    return 0
  fi
  cat >> "$props" <<'EOF'
extra-keys = [['ESC','|','/','HOME','UP','END','PGUP'],['TAB','CTRL','ALT','LEFT','DOWN','RIGHT','PGDN']]
EOF
  ok "extra keys row added"
}

# -------------------------------------------------------------------- nvim

write_nvim_config() {
  mkdir -p "$HOME/.config/nvim" || return 1
  cat > "$HOME/.config/nvim/init.lua" <<'EOF'
-- Minimal defaults written by termux/setup.sh
-- config-version: 2 (setup.sh upgrades older managed configs, with a backup)
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

-- yy / p use nvim's own registers, so they always work. The Android
-- clipboard (needs the Termux:API app) is explicit: <leader>y copies,
-- <leader>v pastes. Do not set clipboard=unnamedplus here: without the
-- Termux:API app every yank/put would hang or paste nothing.
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
  vim.keymap.set({ "n", "v" }, "<leader>y", '"+y', { desc = "yank to Android clipboard" })
  vim.keymap.set("n", "<leader>Y", '"+yy', { desc = "yank line to Android clipboard" })
  vim.keymap.set({ "n", "v" }, "<leader>v", '"+p', { desc = "paste from Android clipboard" })
end

-- insert mode paste from the terminal (long-press paste) works via bracketed
-- paste; this adds an explicit fallback toggle
vim.keymap.set("n", "<leader>p", "<cmd>set paste!<CR>", { desc = "toggle paste mode" })
EOF
}

setup_nvim() {
  have nvim || { warn "nvim not installed"; return 1; }
  local cfg="$HOME/.config/nvim"

  # Upgrade a config this script wrote earlier (never a user's own config).
  if [ -f "$cfg/init.lua" ] \
     && grep -q 'written by termux/setup.sh' "$cfg/init.lua" \
     && ! grep -q 'config-version: 2' "$cfg/init.lua"; then
    backup "$cfg/init.lua"
    write_nvim_config || return 1
    ok "upgraded managed nvim config to v2 (clipboard fix)"
    return 0
  fi

  if [ -f "$cfg/init.lua" ] || [ -f "$cfg/init.vim" ] || [ -d "$cfg/.git" ]; then
    skip "nvim config exists at $cfg"
    return 0
  fi

  if [ -n "$NVIM_CONFIG_REPO" ]; then
    rmdir "$cfg" 2>/dev/null
    git clone --depth 1 "$NVIM_CONFIG_REPO" "$cfg" || return 1
    ok "cloned $NVIM_CONFIG_REPO"
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

  if [ -f "$ssh_dir/id_ed25519" ]; then
    skip "ssh key exists"
  else
    ssh-keygen -t ed25519 -N "" -f "$ssh_dir/id_ed25519" -C "$(whoami)@termux" >/dev/null \
      || return 1
    ok "generated $ssh_dir/id_ed25519"
  fi

  # Only prompt for a GitHub user when nothing is configured yet.
  local user="$GITHUB_USER"
  if [ -z "$user" ] && [ ! -s "$auth" ]; then
    user=$(ask "GitHub username to import ssh keys from (blank to skip): ")
  fi

  if [ -n "$user" ]; then
    local keys=""
    keys=$(curl -fsSL --max-time 15 "https://github.com/$user.keys") || keys=""
    if [ -n "$keys" ]; then
      touch "$auth" && chmod 600 "$auth" || return 1
      printf '%s\n' "$keys" >> "$auth"
      sort -u "$auth" -o "$auth"
      ok "imported keys for $user"
    else
      warn "no keys fetched for '$user'"
    fi
  fi

  if [ ! -s "$auth" ]; then
    warn "no authorized_keys; set a password to log in: run 'passwd'"
  fi

  have termux-wake-lock && termux-wake-lock 2>/dev/null
  if pgrep -x sshd >/dev/null 2>&1; then
    skip "sshd running"
  else
    sshd || { warn "sshd failed to start"; return 1; }
    ok "sshd started"
  fi

  local boot="$HOME/.termux/boot/sshd.sh"
  if [ -x "$boot" ]; then
    skip "boot script exists"
  elif mkdir -p "$HOME/.termux/boot"; then
    cat > "$boot" <<'EOF'
#!/data/data/com.termux/files/usr/bin/sh
termux-wake-lock
sshd
EOF
    chmod +x "$boot" && ok "wrote $boot"
  fi

  local ip=""
  if have ip; then
    ip=$(ip -4 addr show wlan0 2>/dev/null | awk '/inet /{sub("/.*","",$2); print $2; exit}')
  fi
  SSH_HINT="ssh -p 8022 $(whoami)@${ip:-<phone-ip>}"
}

# ------------------------------------------------------------------- shell

write_starship_config() {
  mkdir -p "$HOME/.config" || return 1
  cat > "$HOME/.config/starship.toml" <<'EOF'
# Compact one-line prompt for narrow phone screens.
add_newline = false
command_timeout = 500
format = "$directory$git_branch$git_status$cmd_duration$character"

[directory]
truncation_length = 2
truncation_symbol = "…/"

[git_branch]
symbol = " "

[cmd_duration]
min_time = 2000

[character]
success_symbol = "[❯](green)"
error_symbol = "[❯](red)"
EOF
}

setup_shell() {
  # Login shell.
  if [ "$SHELL_CHOICE" != bash ]; then
    have "$SHELL_CHOICE" || { warn "$SHELL_CHOICE not installed"; return 1; }
    local cur=""
    cur=$(readlink "$HOME/.termux/shell" 2>/dev/null || true)
    if [ "$(basename "${cur:-bash}")" = "$SHELL_CHOICE" ]; then
      skip "login shell is $SHELL_CHOICE"
    else
      chsh -s "$SHELL_CHOICE" && ok "login shell set to $SHELL_CHOICE" \
        || warn "chsh failed; run: chsh -s $SHELL_CHOICE"
    fi
  fi

  # Shell plugins (zsh only).
  if [ "$SHELL_CHOICE" = zsh ]; then
    add_block "$HOME/.zshrc" "toolbox zsh plugins" \
'[ -f "$PREFIX/share/zsh-autosuggestions/zsh-autosuggestions.zsh" ] && source "$PREFIX/share/zsh-autosuggestions/zsh-autosuggestions.zsh"
[ -f "$PREFIX/share/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh" ] && source "$PREFIX/share/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh"' \
      || return 1
  fi

  # Prompt.
  [ "$PROMPT" = starship ] || { ok "prompt: none requested"; return 0; }
  have starship || { warn "starship not installed"; return 1; }

  if [ -f "$HOME/.config/starship.toml" ]; then
    skip "starship.toml exists"
  else
    write_starship_config || return 1
    ok "wrote ~/.config/starship.toml"
  fi

  case "$SHELL_CHOICE" in
    bash) add_block "$HOME/.bashrc" "toolbox starship" \
            'command -v starship >/dev/null 2>&1 && eval "$(starship init bash)"' ;;
    zsh)  add_block "$HOME/.zshrc" "toolbox starship" \
            'command -v starship >/dev/null 2>&1 && eval "$(starship init zsh)"' ;;
    fish) add_block "$HOME/.config/fish/config.fish" "toolbox starship" \
            'if type -q starship
    starship init fish | source
end' ;;
  esac
}

# -------------------------------------------------------------------- main

preflight

step "Packages" install_packages

if [ "${SKIP_FONT:-0}" != 1 ]; then
  step "Nerd Font" install_font
  step "Extra keys row" termux_props
fi

[ "${SKIP_NVIM:-0}"  != 1 ] && step "Neovim config" setup_nvim
[ "${SKIP_SSH:-0}"   != 1 ] && step "SSH" setup_ssh
[ "${SKIP_SHELL:-0}" != 1 ] && step "Shell ($SHELL_CHOICE, prompt: $PROMPT)" setup_shell

have termux-reload-settings && termux-reload-settings 2>/dev/null

printf '\n==================== summary ====================\n'
for s in "${DONE[@]:-}";   do [ -n "$s" ] && printf '  [ok]     %s\n' "$s"; done
for s in "${FAILED[@]:-}"; do [ -n "$s" ] && printf '  [FAILED] %s\n' "$s"; done
[ -n "$SSH_HINT" ] && printf '\nConnect from another machine:\n  %s\n' "$SSH_HINT"
cat <<'EOF'

Manual follow-ups:
  - Restart Termux to apply the font, shell and prompt.
  - Clipboard in nvim needs the Termux:API app (F-Droid).
  - ssh autostart on boot needs the Termux:Boot app (F-Droid), opened once.
  - Re-running this script is safe; it only adds what is missing.
EOF

[ "${#FAILED[@]}" -eq 0 ]
