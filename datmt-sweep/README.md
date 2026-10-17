# datmt-sweep

An interactive terminal tool for cleaning a Linux box and finding what drains its battery. It grew out of a small Arch orphan-package picker and now covers packages, containers, caches, big files, and power use.

Written in Go with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

> **Status:** working on Arch (the author's machine). The apt, dnf, zypper, and snap backends are written against fixtures but have **not been run on a real system**. See [Verification status](#verification-status).

## What it does

Each sidebar entry is a **category**: a read-only scan that lists items with sizes, a preview pane, a dry run, and a removal action.

| Category | What it lists | Removal |
|---|---|---|
| **Packages** | Orphans and leaf packages (pacman, apt, dnf, zypper) | Package manager remove (`sudo`) |
| **Containers / Images / Volumes** | Docker containers, unused images, unused volumes | `docker rm` / `rmi` / `volume rm`, never `-f` |
| **Flatpak** | Apps and unused runtimes | `flatpak uninstall` |
| **Snap** | Disabled revisions, apps | `snap remove` |
| **Journal** | systemd journal vacuum presets | `journalctl --vacuum-*` (`sudo`) |
| **Services** | systemd services ranked by CPU and wakeups, with estimated watts | `systemctl stop` |
| **Processes** | Processes ranked by activity | `SIGTERM` |
| **Ports** | Listening and established TCP/UDP sockets mapped to processes | Kill the process or stop its unit |
| **Tunables** | Power settings: PCIe ASPM, USB autosuspend, Wi-Fi power save, HDA audio, SATA link power, NMI watchdog | Applied via one `sudo` batch |
| **Big files** | Files over a size threshold under `$HOME` | Trash, or permanent delete (`D`) |
| **Caches** | Entries in `~/.cache` | Permanent delete |
| **Trash** | The freedesktop trash | Permanent delete |
| **Monitor** | Live power tab, see [below](#monitor-tab) | none (view only) |

Only the categories whose tools exist on your machine show up (no `docker` binary, no Docker categories).

## Install

Requires Go (see `go.mod`).

```sh
git clone <repo> && cd datmt-sweep
make build            # produces ./datmt-sweep
sudo install -m755 datmt-sweep /usr/local/bin/
```

Packaging configs exist but are untested: `.goreleaser.yaml` (tar.gz, deb, rpm for amd64 and arm64) and `packaging/aur/PKGBUILD`.

## Usage

```sh
datmt-sweep                      # interactive TUI
datmt-sweep --clean              # safe-clean mode, confirms per category
datmt-sweep --clean --dry-run    # show what safe-clean would do, change nothing
datmt-sweep --json               # scan everything, print JSON, exit (read-only)
datmt-sweep --json --category docker
datmt-sweep --version
```

| Flag | Meaning |
|---|---|
| `--all` | Offer every installed package, not just unused ones |
| `--min-size N` | Big-file threshold in MiB (default 100) |
| `--clean` | Safe-clean mode (needs an interactive terminal) |
| `--dry-run` | With `--clean`: print the plan only |
| `--json` | Print all scan results as JSON and exit |
| `--category S` | With `--json`: only categories whose name contains `S` (case-insensitive) |

### TUI keys

| Key | Action |
|---|---|
| `tab` / `shift+tab`, `l` / `h` | Next / previous category |
| `j` / `k`, arrows, `pgup` / `pgdn`, `g` / `G` | Move |
| `space` | Select item |
| `a` | Select all (never selects items graded *danger*) |
| `d` | Clear selection |
| `s` | Toggle sort: size / name |
| `/` | Filter |
| `i` | Add the item to the ignore list |
| `enter` | Remove selected (shows a dry run, then asks `y/N`) |
| `D` | Delete **permanently**. Only for categories that normally trash (Big files). |
| `m` | Jump to the Monitor tab |
| `p` | Pause the Monitor |
| `q` / `esc` | Quit |

With nothing selected, `enter` acts on the item under the cursor.

### Safe-clean mode

`--clean` gathers every item graded **safe**, shows each category's dry run, and asks `[y]es / [n]o / [a]ll remaining / [q]uit` before removing anything. There is intentionally no `--yes`, and closing stdin counts as quit.

What counts as safe:

- Orphaned packages (auto-installed, nothing depends on them)
- Docker images not used by any container
- Unused Flatpak runtimes
- Disabled snap revisions
- `~/.cache` entries
- Trash contents (this is permanent)

Never included: big files, services, processes, ports, tunables, Docker volumes, journal, leaf packages.

Note that unused Docker images include tagged images you pulled deliberately. They only cost a re-pull, but they are not limited to dangling ones.

### Monitor tab

A live view, refreshed every 2 seconds:

- Battery percentage, discharge or charge rate in watts, time left or time to full, mains state, health (full vs design capacity), cycle count
- Whole-machine CPU use and, if readable, CPU package power from RAPL
- A sparkline of the last 4 minutes
- Top consumers, grouped by command name, with CPU %, wakeups per second and estimated watts

**The per-process watts are estimates.** With RAPL readable, package power is split by each command's CPU share. Without it, and while discharging, the battery draw is split the same way, which attributes idle hardware to whoever uses CPU and so overstates. The tab header says which source is in use. On recent kernels RAPL needs root (`energy_uj` is mode 0400), so run as root if you want the RAPL figure. There is no GPU power reading.

### JSON output

`--json` runs every scanner to completion and prints one document:

```json
{
  "generated": "2026-10-14T03:44:33Z",
  "categories": [
    {
      "name": "Caches",
      "total_bytes": 25175229490,
      "items": [
        { "id": "/home/me/.cache/uv", "title": "uv", "size_bytes": 8900000000, "risk": "safe" }
      ]
    }
  ]
}
```

Items on the ignore list are included with `"ignored": true` and left out of `total_bytes`. Items measured in watts rather than bytes are also left out of totals. A failed scan sets `"error"` on its category.

## Safety model

- **Scanners are read-only and unprivileged.** Only a category's removal action changes anything.
- **Dry run first.** Every removal shows what it would do before the `y/N` prompt. For packages, the tool asks the package manager rather than recomputing dependencies itself.
- **Protected packages.** Core packages are never offered for removal. The protect set is derived from the system where possible (pacman `base` and `HoldPkg`/`IgnorePkg`, the kernel owner, Debian Essential/Priority, Fedora `protected.d`) with a hand-written regex as a floor.
- **Risk grades.** Items are *safe*, *review*, or *danger*. Danger items (Docker volumes, protected services, sockets that cannot be acted on) are never bulk-selected.
- **Path guard.** File deletion refuses anything outside `$HOME` or on the protected list.
- **Trash vs delete.** Big files go to the trash by default. Trashing on the same filesystem frees **no space** until the trash is emptied, so use `D` to delete outright. Caches and the trash bin are always permanent, because trashing them would not free space either.
- **`sudo` per batch.** There is no helper process. Privileged work runs `sudo ...` with your terminal attached.
- **Audit log.** Every removal writes a `confirm` record before and a `result` record after.

## Files

| Path | Purpose |
|---|---|
| `$XDG_CONFIG_HOME/datmt-sweep/ignore.toml` (default `~/.config/...`) | Ignore list, written by the `i` key |
| `$XDG_STATE_HOME/datmt-sweep/actions.log` (default `~/.local/state/...`) | Audit log, JSON lines, no rotation |

The ignore file is a small TOML subset. Entries match on item ID with shell globs, or on an absolute path prefix:

```toml
[ignore]
Caches = ["/home/me/.cache/google-chrome", "*/JetBrains"]
Packages = ["linux-lts"]
```

To un-ignore something, edit the file by hand.

## Verification status

What has actually been run:

- **Arch / pacman, Docker, Flatpak, systemd, journal, files, ports, power:** live scans on the author's machine; unit tests pass (`go test ./...`).
- **`--json`, `--clean --dry-run`, and the Monitor sampler:** run live.

What has **not** been run on a real system:

- **apt, dnf, zypper, snap** backends: written and tested against recorded fixtures only. The `dnf` and `zypper` dry-run commands are untried as a non-root user.
- **A real `y` in safe-clean** and the **permanent-delete** key `D` in the TUI: covered by unit tests, not exercised end to end.
- **Packaging** (goreleaser, PKGBUILD): never built.
- The **TUI as a whole** has not been used much by a human in a real terminal.

Known gaps: Podman is not supported; Docker needs the `docker` group (no `sudo` fallback); the Docker build cache and `/var/cache/pacman` are not categories; trashing across filesystems fails rather than copying; the big-file scan only walks `$HOME`; no GPU power; the audit log is never rotated.

Live tests that touch the real system are gated behind a build tag and are read-only: `go test -tags live ./...`.

## Development

```sh
make test      # go vet + go test
make build
go test -tags live ./internal/pkgs ./internal/containers ./internal/journal
```

Layout: each area is a package under `internal/` implementing `core.Category` (name, scanner, info, dry run, remove). Add a new category by implementing that interface and registering it in `cmd/datmt-sweep/main.go`. A category whose removal can be made permanent can also implement `core.PermanentRemover`.

## License

Not chosen yet. There is no LICENSE file.
