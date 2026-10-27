#!/usr/bin/env bash
# Patch run-or-raise@edvard.cz for GNOME 51.
#   ./fix-run-or-raise.sh          -> per-user copy in ~/.local/share (run as each affected user)
#   sudo ./fix-run-or-raise.sh     -> patch /usr/share in place, fixes all users (undone by package update)
set -euo pipefail

uuid=run-or-raise@edvard.cz
src=/usr/share/gnome-shell/extensions/$uuid

if [[ $EUID -eq 0 ]]; then
  dst=$src
else
  dst=$HOME/.local/share/gnome-shell/extensions/$uuid
  mkdir -p "$dst"
  cp -r "$src"/. "$dst"/
  chmod -R u+w "$dst"
fi

# 1. declare 51 supported
grep -q '"51"' "$dst/metadata.json" || sed -i 's/"50"/"50",\n    "51"/' "$dst/metadata.json"
# 2. Clutter.get_default_backend() removed in GNOME 51
sed -i 's/Clutter\.get_default_backend()\.get_default_seat()/global.stage.context.get_backend().get_default_seat()/' "$dst/extension.js"

echo "Patched $dst. Log out and back in, then:"
echo "  gnome-extensions enable $uuid"
echo "  gnome-extensions info $uuid   # want State: ACTIVE"
