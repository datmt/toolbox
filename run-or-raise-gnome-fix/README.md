# run-or-raise-gnome-fix

Fixes the [run-or-raise](https://github.com/CZ-NIC/run-or-raise) GNOME extension (`run-or-raise@edvard.cz`) on GNOME Shell 51, where it shows as out of date / `State: ERROR`.

It adds `"51"` to `metadata.json` and replaces the removed `Clutter.get_default_backend()` call.

## Use

Per user (copies the extension to `~/.local/share/gnome-shell/extensions/` and patches the copy):

```sh
curl -fsSL https://raw.githubusercontent.com/datmt/toolbox/main/run-or-raise-gnome-fix/fix.sh | bash
```

All users (patches `/usr/share` in place; a package update overwrites it):

```sh
curl -fsSL https://raw.githubusercontent.com/datmt/toolbox/main/run-or-raise-gnome-fix/fix.sh | sudo bash
```

Then log out and back in (Wayland), and:

```sh
gnome-extensions enable run-or-raise@edvard.cz
gnome-extensions info run-or-raise@edvard.cz   # State: ACTIVE
```

Requires the extension package to be installed in `/usr/share/gnome-shell/extensions/`.
