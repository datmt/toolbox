# termux

One script that sets up a fresh Termux install: Nerd Font, neovim with sane defaults, and an ssh server.

```sh
curl -fsSL https://raw.githubusercontent.com/datmt/toolbox/main/termux/setup.sh | bash
```

Read it first if you like: [`setup.sh`](setup.sh). It is safe to re-run.

## What it does

1. **Packages**: updates Termux, installs `git curl unzip tmux neovim ripgrep fd clang make nodejs termux-api openssh`.
2. **Font**: installs FiraCode Nerd Font Mono to `~/.termux/font.ttf` (single file from this repo, falls back to the Nerd Fonts v3.5.1 release zip). Existing font is backed up.
3. **Extra keys**: adds an Esc/Tab/Ctrl/Alt/arrows row, unless you already set one.
4. **Neovim**: writes `~/.config/nvim/init.lua` if you have no config: line numbers, syntax highlighting, 2-space tabs, smart-case search, mouse, Android clipboard paste (via Termux:API), `<space>p` paste toggle.
5. **SSH**: generates an ed25519 key, optionally imports your GitHub public keys, starts `sshd` on port **8022**, writes a Termux:Boot autostart script, and prints the connect command.

Each stage runs independently. A failure is reported in the summary and does not stop the rest. Exit code is non-zero if anything failed.

## Options

Set as environment variables:

| Variable | Effect |
| --- | --- |
| `GITHUB_USER=name` | import ssh keys from `github.com/name.keys` without prompting |
| `NVIM_CONFIG_REPO=url` | clone this repo as your nvim config instead of the built-in one (old config moved to `.bak.*`) |
| `SKIP_SSH=1` `SKIP_NVIM=1` `SKIP_FONT=1` | skip that stage |
| `SKIP_UPGRADE=1` | skip `pkg upgrade` (faster) |
| `RAW_BASE=url` | fetch the font from somewhere else (e.g. a fork) |

```sh
GITHUB_USER=datmt SKIP_UPGRADE=1 bash -c "$(curl -fsSL https://raw.githubusercontent.com/datmt/toolbox/main/termux/setup.sh)"
```

## After it runs

- Restart Termux to see the font.
- Install **Termux:API** (F-Droid) for clipboard in nvim.
- Install **Termux:Boot** (F-Droid) and open it once for ssh autostart.
- Connect: `ssh -p 8022 <user>@<phone-ip>`. Run `passwd` first if you did not import keys.

## Font license

`fonts/FiraCodeNerdFontMono-Regular.ttf` is from [Nerd Fonts](https://github.com/ryanoasis/nerd-fonts) v3.5.1. Fira Code is licensed under the SIL OFL, see [`fonts/LICENSE`](fonts/LICENSE).
