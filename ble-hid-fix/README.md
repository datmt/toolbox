# ble-hid-fix

Fixes a Bluetooth LE mouse or keyboard that shows as paired and trusted but does nothing, with this in `journalctl -u bluetooth`:

```
src/service.c:service_accept() input-hog profile accept failed for XX:XX:XX:XX:XX:XX
```

## Cause

BLE input devices (HID over GATT) need the `uhid` kernel module. It loads on demand, the first time such a device connects.

On Arch (and Manjaro, EndeavourOS) a kernel upgrade deletes the old kernel's modules from `/lib/modules/<version>/`. Until you reboot, the running kernel can't load any module it hasn't loaded yet, so `uhid` fails and so does the device. Debian, Ubuntu and Fedora keep old kernels installed side by side, so it rarely happens there.

## Use

```sh
curl -fsSL https://raw.githubusercontent.com/datmt/toolbox/main/ble-hid-fix/fix.sh -o fix.sh
sh fix.sh              # lists devices
sh fix.sh <MAC>        # connects that device
```

The script:

1. Checks whether the running kernel's modules are on disk. If not, it tells you to reboot (the fix in most cases).
2. Loads `uhid` and restarts `bluetooth`.
3. Connects the device and shows the last log lines.

Still not connecting? Re-pair:

```sh
bluetoothctl remove <MAC>
bluetoothctl scan le        # put device in pairing mode, Ctrl+C once seen
bluetoothctl pair <MAC>
bluetoothctl trust <MAC>
bluetoothctl connect <MAC>
```

## Prevent it

Reboot after a kernel upgrade, or on Arch install `kernel-modules-hook`, which keeps the running kernel's modules until reboot.
