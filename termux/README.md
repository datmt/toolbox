# termux

One script that sets up a fresh Termux install: Nerd Font, neovim with sane defaults, an ssh server, and a starship prompt.

```sh
curl -fsSL https://raw.githubusercontent.com/datmt/toolbox/main/termux/setup.sh | bash
```

Read it first if you like: [`setup.sh`](setup.sh). It is safe to re-run: each stage checks what is already in place and only adds what is missing.

## What it does

1. **Packages**: installs only the missing ones of `git curl unzip tmux neovim ripgrep fd clang make nodejs termux-api openssh starship` (plus `zsh`/`fish` if chosen). Nothing missing means no `pkg update`.
2. **Font**: installs FiraCode Nerd Font Mono to `~/.termux/font.ttf` (single file from this repo, falls back to the Nerd Fonts v3.5.1 release zip). An existing font is left alone unless `FORCE_FONT=1`.
3. **Extra keys**: adds an Esc/Tab/Ctrl/Alt/arrows row, unless you already set one.
4. **Neovim**: writes `~/.config/nvim/init.lua` if you have no config: line numbers, syntax highlighting, 2-space tabs, smart-case search, mouse, `yy`/`p` use nvim registers; `<space>y` / `<space>v` copy/paste via the Android clipboard (needs the Termux:API app); `<space>p` toggles paste mode.
5. **SSH**: generates an ed25519 key if absent, optionally imports your GitHub public keys (asks only when `authorized_keys` is empty), starts `sshd` on port **8022** if not running, writes a Termux:Boot autostart script, and prints the connect command.
6. **Shell and prompt**: installs [starship](https://starship.rs) with a compact one-line config (`~/.config/starship.toml`, written only if absent) and hooks it into your shell rc file inside a marked block, added once. With `SHELL_CHOICE=zsh` it also sets zsh as login shell and enables autosuggestions and syntax highlighting; with `fish` it sets fish up.

Each stage runs independently. A failure is reported in the summary and does not stop the rest. Exit code is non-zero if anything failed.

## Options

Set as environment variables:

| Variable | Effect |
| --- | --- |
| `GITHUB_USER=name` | import ssh keys from `github.com/name.keys` without prompting |
| `NVIM_CONFIG_REPO=url` | clone this repo as your nvim config instead of the built-in one (skipped if `~/.config/nvim` already has a config) |
| `SHELL_CHOICE=bash\|zsh\|fish` | shell to set up (default `bash`, login shell untouched) |
| `PROMPT=starship\|none` | prompt (default `starship`) |
| `SKIP_SSH=1` `SKIP_NVIM=1` `SKIP_FONT=1` `SKIP_SHELL=1` | skip that stage |
| `FORCE_FONT=1` | replace an existing `~/.termux/font.ttf` |
| `SKIP_UPGRADE=1` | skip `pkg upgrade` (faster) |
| `RAW_BASE=url` | fetch the font from somewhere else (e.g. a fork) |

```sh
GITHUB_USER=datmt SHELL_CHOICE=fish bash -c "$(curl -fsSL https://raw.githubusercontent.com/datmt/toolbox/main/termux/setup.sh)"
```

## After it runs

- Restart Termux to see the font.
- Install **Termux:API** (F-Droid) for clipboard in nvim.
- Install **Termux:Boot** (F-Droid) and open it once for ssh autostart.
- Connect: `ssh -p 8022 <user>@<phone-ip>`. Run `passwd` first if you did not import keys.

## Font license

`fonts/FiraCodeNerdFontMono-Regular.ttf` is from [Nerd Fonts](https://github.com/ryanoasis/nerd-fonts) v3.5.1. Fira Code is licensed under the SIL OFL, see [`fonts/LICENSE`](fonts/LICENSE).
