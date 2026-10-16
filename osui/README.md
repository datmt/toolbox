# osui

A local web UI for [OpenSnitch](https://github.com/evilsocket/opensnitch), written in Go. It replaces the Qt `opensnitch-ui` for two jobs:

- answering connection prompts with narrow rules instead of "allow this program everywhere", and
- managing rules, especially **per-program allowlists of domains / IPs / networks**.

OpenSnitch's daemon (`opensnitchd`) is a gRPC *client* that connects to a UI server. `osui` is that server, so it needs no root and never touches `/etc/opensnitchd` directly. The daemon saves the rules itself.

> Status: tested against a fake daemon (Go end-to-end tests and JS logic tests). The browser UI and the real-daemon path are young, so keep an eye on them. Tested with OpenSnitch daemon v1.7.2.

## Quick start

Requires Go and a running `opensnitchd`.

```sh
./run.sh              # build if needed, stop opensnitch-ui if running, start osui, print the URL
./run.sh --keep-ui    # refuse to start while opensnitch-ui is running
./run.sh demo         # fake daemon fires sample prompts; the real firewall is untouched
```

Open the printed `http://127.0.0.1:8899/?token=…` URL (the token is kept in `~/.config/osui/token`, so the URL is stable across restarts). If `osui` is already running, `./run.sh` just prints the URL again.

The daemon must point at the socket osui listens on: `Server.Address` in `/etc/opensnitchd/default-config.json` (default `unix:///tmp/osui.sock`, which is what `osui` uses).

> **While no UI is connected**, connections that match no rule follow `DefaultAction` in the daemon config. If that is `"allow"`, set it to `"deny"` so a missing UI fails closed.

## Using it

### Prompts
When a connection has no matching rule, a card appears (plus a desktop notification if `notify-send` exists). Choose a scope, a duration, then Allow or Deny. The default scope is the narrowest one: *program → exact host + port*. "Program → anything" is marked red.

### Allowlists
This is the tab for "let `curl` reach only these sites".

1. Pick a program (or type its path).
2. Type destinations: domains (`github.com`), wildcards (`*.example.org`), IPs (`93.184.216.34`), CIDR networks (`10.0.0.0/8`). Separate several with spaces or commas. Optionally add a port.
3. Click **Add**. Each entry becomes one allow rule.
4. Turn on **Block everything else for this program**. This creates one deny rule for the program.
5. If the program already has a broad allow rule (for example the old "allow curl everywhere"), the tab offers to disable it. Do that, or the allowlist is bypassed.

Broad allow rules are flagged in the **Rules** tab, with a **Restrict…** button that jumps to the allowlist for that program.

### Rules and Connections
- **Rules**: search, filter by action or risk (`broad` = no destination restriction), enable/disable, bulk delete, and a JSON editor.
- **Connections**: recent traffic reported by the daemon. **Rule from this** opens a prefilled narrow rule.

## How rule matching works (why names look odd)

The daemon walks rules **in name order** and returns the first match that is a Deny, Reject or `precedence` rule. Otherwise the last allow match is used. So the allowlist uses these names:

| Rule | Name | precedence |
|------|------|------------|
| allow entry | `wl-<program>-a-<destination>` | yes |
| deny the rest | `wl-<program>-z-deny-rest` | no |

`-a-` sorts before `-z-`, so a matching allow entry always wins before the deny rule is reached. Don't rename them.

Domain matching uses the hostname the daemon saw in the DNS lookup. With DNS-over-HTTPS in an app, or a hardcoded IP, the hostname is unknown, so add the IP instead.

## Security model

The UI can change your firewall, so:

- listens on loopback only (`--listen` must be 127.0.0.1 / localhost / ::1);
- every request needs the secret token (HttpOnly, SameSite=Strict cookie);
- `Host` and `Origin` are checked (blocks DNS rebinding and cross-site requests);
- strict CSP; all dynamic text is inserted with `textContent` (process paths, hostnames and arguments are attacker-influenced);
- rules are validated server-side (name, action, duration, operator type, regexp compiles).

The gRPC socket is created with mode `0600`. `osui` refuses to start if another UI still owns it.

## Development

```sh
go test -race ./...               # e2e tests: fake daemon over gRPC + HTTP handler in-process
node test/ui_logic.test.mjs       # allowlist/scope logic, no browser needed
go run ./cmd/fakedaemon           # sample prompts for a manually started `osui --socket unix:///tmp/osui-demo.sock`
```

Layout:

```
main.go                   flags, token, wiring
internal/server           gRPC UI service the daemon talks to
internal/hub              shared state: prompts, rules cache, notifications to the daemon
internal/web              HTTP API, auth, rule validation/risk, embedded static UI
internal/pb, proto/       generated from OpenSnitch's ui.proto (v1.7.2)
cmd/fakedaemon            pretend daemon for demos
```

Regenerate the protobuf code with `protoc` plus `protoc-gen-go` and `protoc-gen-go-grpc`, with `--go_opt=Mui.proto=osui/internal/pb` (and the same for `--go-grpc_opt`).

## Limitations

- Rules and prompts only. It does not manage the system firewall, toggle interception, or edit daemon config.
- The rule list is the daemon's list at connect time plus changes made through osui. Edits made elsewhere show up after the daemon reconnects.
- No autostart yet (a systemd user unit would do it).

## License

`proto/ui.proto` and the code generated from it (`internal/pb`) are derived from OpenSnitch, which is GPL-3.0. Decide on this project's license with that in mind.
