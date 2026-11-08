# singbox

Route chosen domains (GitHub, Claude, Gemini, OpenAI, X) through an SSH server (a "gateway", e.g. a box in another country) with [sing-box](https://sing-box.sagernet.org/). Everything else goes direct. No separate `ssh -D` tunnel: sing-box has a built-in `ssh` outbound.

How it works: a TUN device captures all traffic, sing-box sniffs the TLS domain, matches `domain_suffix`, and sends matches out through the SSH server.

Tested on Arch with sing-box 1.14 and Tailscale. Needs 1.12+ config format.

## Setup

1. Install:
   ```sh
   sudo pacman -S sing-box
   ```
2. Copy `config.example.json` to `config.json` and fill in the placeholders:

   | Placeholder | Value |
   | --- | --- |
   | `GATEWAY_TAILSCALE_IP` | Gateway address (Tailscale IP or public IP) |
   | `GATEWAY_USER` | SSH user on the gateway |
   | `private_key_path` | Key the gateway accepts. Must be **unencrypted** and an **absolute path** (sing-box runs as root). Check which key it takes with `ssh -v user@gateway true` |
   | `host_key` | Gateway host keys, see below |

3. Get the host keys. The server may present either type, so pin both:
   ```sh
   ssh-keyscan -t ed25519,ecdsa GATEWAY_IP
   ```
   Put each line in `host_key` as `"<type> <base64>"` (drop the leading IP). The type prefix is required, base64 alone fails with `ssh: no key found`.

4. Check and install:
   ```sh
   sudo sing-box check -c config.json
   sudo cp config.json /etc/sing-box/config.json
   sudo systemctl enable --now sing-box
   ```

## Verify traffic uses the proxy

sing-box only logs errors for the ssh outbound, not successes. Check from the gateway side instead:

```sh
curl -s --limit-rate 1k -m 8 -o /dev/null https://github.com/git/git/archive/refs/heads/master.zip &
sleep 3
ssh GATEWAY_USER@GATEWAY_IP 'ss -tn | grep -E "20\.205|140\.82"'
```

GitHub IPs in the output mean the traffic exits through the gateway.

## Add or remove domains

Edit the `domain_suffix` list in `config.json` (subdomains match automatically), then:

```sh
sudo cp config.json /etc/sing-box/config.json
sudo sing-box check -c /etc/sing-box/config.json && sudo systemctl restart sing-box
```

Put IPs in their own rule (`{"ip_cidr": ["1.2.3.4/32"], "outbound": "gw"}`). Fields in one rule are ANDed.

## Gotchas

- **Tailscale gateway: the `ts` outbound is required.** `auto_detect_interface` binds sockets to the physical NIC, so a Tailscale IP is unreachable and the dial hangs (`dial tcp 100.x:22: i/o timeout`). The `ts` outbound binds to `tailscale0`, and `gw` uses it as `detour`. If your gateway has a public IP, drop `ts` and `detour`.
- **`route_exclude_address` keeps Tailscale and LAN out of the TUN.** Tailscale's ip rules take priority anyway, and excluding it avoids loops. This also means Tailscale IPs can't be routed via the TUN. To reach a Tailscale host through the gateway, use SSH `ProxyJump` in `~/.ssh/config`.
- **Auth fails with `attempted methods [none publickey]`:** sing-box offers only the key in `private_key_path`. Use the key the gateway has in `authorized_keys`.
- **`host key mismatch, server send ecdsa-...`:** pin that key type too.
- **Routing your own tools through the proxy:** if the gateway breaks, the proxied domains break with it. `sudo systemctl stop sing-box` restores direct access.
- **Log noise:** `i/o timeout` errors on `outbound/direct` to random IPs and ports are usually torrent clients connecting to dead peers, not a sing-box fault.
