# omada-autologin

[![CI](https://github.com/pritkr/omada-autologin/actions/workflows/ci.yml/badge.svg)](https://github.com/pritkr/omada-autologin/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/pritkr/omada-autologin)](https://github.com/pritkr/omada-autologin/releases)

Keep a [TP-Link Omada](https://www.tp-link.com/en/omada-sdn/) captive-portal
session alive on **macOS, Linux and Windows**.

Campus and guest Wi-Fi networks built on Omada often log a client out when a
data quota resets, when the controller ages the session, or when the machine
sleeps. This tool discovers the Omada portal entry URL that the gateway
redirects to and re-authenticates with **your own** credentials. It
runs as one small native daemon: a static Go binary, no runtime, no timer
spawning a new process every few seconds.

| Platform | Service | Health check |
|---|---|---|
| macOS | `launchd` LaunchAgent with `KeepAlive` | 60 s online, 5–60 s adaptive retry |
| Linux | `systemd --user` service + netlink watcher | 60 s online, **instant on link/address change**, 5–60 s retry |
| Windows | Task Scheduler logon task with restart-on-failure | 60 s online, 5–60 s adaptive retry |

## Install

### One command (Linux and macOS)

```sh
curl -fsSL https://raw.githubusercontent.com/pritkr/omada-autologin/main/install.sh | sh
```

The installer:

1. detects OS and CPU, downloads the matching release archive,
2. verifies it against the release's `SHA256SUMS.txt`,
3. installs the binary to `~/.local/bin/omada-autologin`,
4. prompts for your password (or reads `OMADA_PASSWORD`; `OMADA_HOST` and
   `OMADA_USERNAME` are optional),
5. runs one login test, and
6. installs and starts the background service.

Pass options after `sh -s --`:

```sh
curl -fsSL https://raw.githubusercontent.com/pritkr/omada-autologin/main/install.sh \
  | sh -s -- --no-service
```

| Option | Meaning |
|---|---|
| `--no-service` | Configure and test, but do not install the daemon. |
| `--no-login` | Skip the initial login test. |
| `--dir PATH` | Install the binary somewhere other than `~/.local/bin`. |
| `--version TAG` | Install a specific release, for example `v0.1.2`. |

For unattended setup:

```sh
OMADA_PASSWORD='your-password' \
OMADA_HOST='192.168.1.3' \
  sh -s -- --no-login < install.sh
```

`OMADA_USERNAME` is optional. Set it only when the portal usernames share a
fixed prefix; the last three digits still rotate.

Run the script as your normal user, **not** with `sudo`.

### Windows

In PowerShell:

```powershell
irm https://raw.githubusercontent.com/pritkr/omada-autologin/main/install.ps1 | iex
```

It downloads and checksum-verifies the release, installs to
`%LOCALAPPDATA%\omada-autologin\bin`, adds that directory to your user `PATH`,
runs a login test, and creates the logon task.

To pass options, download the script first:

```powershell
.\install.ps1 -NoService          # configure only
.\install.ps1 -NoLogin            # skip the login test
.\install.ps1 -InstallDir 'D:\tools\omada'
.\install.ps1 -Version v0.1.2
```

### Prebuilt binaries

Download the archive for your OS and CPU from
[Releases](https://github.com/pritkr/omada-autologin/releases), unpack it, and
put `omada-autologin` somewhere permanent on your `PATH` **before** running
`install`, because the service stores the absolute path to the binary.

### With Go

```sh
go install github.com/pritkr/omada-autologin@latest
```

Requires Go 1.23 or newer.

### From source

```sh
git clone https://github.com/pritkr/omada-autologin.git
cd omada-autologin
go build -trimpath -o omada-autologin .
```

## Quick start

One command creates the config, checks the portal, and starts the daemon:

```sh
omada-autologin
```

No arguments or flags are needed. It prompts for the portal password, then
installs and starts the background service. The username is generated
automatically; you do not need a registration number.

For a fully non-interactive run, or to pass options, use `setup`:

```sh
omada-autologin setup --password 'your-password'
```

| Option | Meaning |
|---|---|
| `--host H` | Omada controller/gateway host (default `192.168.1.3`). |
| `--username U` | Optional fixed prefix; only its last 3 digits rotate. |
| `--interval SECONDS` | Check interval while online. |
| `--no-login` | Skip the initial login test. |
| `--no-service` | Configure and test, but do not install the daemon. |
| `--force` | Rewrite an existing config. |

Re-running `setup` keeps the existing config, tests the portal again, and
restarts the service, so it also works as an "apply and restart" command.

To do the same steps by hand:

```sh
omada-autologin init --host 192.168.1.3   # create config (prompts for password)
omada-autologin once --dry-run            # inspect, no credentials sent
omada-autologin once                      # log in once
omada-autologin install                   # install and start the daemon
```

Check it at any time:

```sh
omada-autologin status
omada-autologin discover
omada-autologin status --json
```

Manual login with a fresh URL from the browser (useful for testing):

```sh
omada-autologin login --url 'https://192.168.1.3:8843/portal/entry?cid=...&ap=...&ssid=...&rid=1'
```

## How it works

One long-lived daemon checks connectivity and logs in only when the network is
captive. It does not run a login request every minute, and it does not spawn a
new process on a timer.

### Adaptive schedule

| Situation | Next check |
|---|---|
| Online | 60 s (configurable, minimum 5 s) |
| Offline or rejected | 5 s, then 10, 20, 40, capped at 60 s |
| Recovered | back to 60 s |
| Linux link/address change | immediately (netlink wake) |
| Five consecutive login failures | login attempts pause for 5 minutes; health checks continue |
| Daemon crash | service manager restarts it |

On Linux the daemon opens an `AF_NETLINK` route socket and wakes as soon as
NetworkManager, systemd-networkd, iwd, or `ip link` reports a link or address
change. A new Wi-Fi association is therefore handled in about a second without
polling. The wake also clears the five-minute failure cooldown, because
failures from the previous network should not delay the first login on the new
one. macOS and Windows use the normal interval; both resume immediately after
sleep because the pending timer fires on wake.

### Discovery

1. Request each `check_url` (plain HTTP, because HTTPS is usually bypassed by
   captive portals).
2. Look for a captive-portal redirect in `Location`.
3. If the gateway answers `200` in place, search the body for an Omada
   `/portal/entry?...` URL in JavaScript, a meta refresh, or a plain link.
   Escaped slashes (`https:\/\/...`) and `&amp;` entities are normalised.
4. If no URL can be parsed, reuse `portal_url` or the last successful URL from
   state. Many controllers keep accepting a recent entry URL.
5. Validate the host against `portal_hosts`, extract `cid` (client MAC), `ap`
   or `gateway` MAC, `ssid`, and `rid`, then authenticate.
6. Confirm the login with a follow-up connectivity probe before calling it a
   success.

### What it sends

The Omada portal uses one of several authentication methods. The tool asks the
controller which one is active via `POST /portal/getPortalPageSetting`, then
uses the matching endpoint:

| `authType` | Method | Endpoint | Payload |
|---|---|---|---|
| `2`, `8` | RADIUS / external | `/portal/radius/auth` | `username`, `password` |
| `5` | Local user | `/portal/auth` | `localuser`, `localuserPsw` |
| `1` | Simple password | `/portal/auth` | `simplePassword` |
| `0` | No auth | `/portal/auth` | — |
| `11` | Hotspot | picks local (5) then RADIUS (8) from `enabledTypes` | as above |
| `3`, `6` | Voucher / SMS | — | rejected with a clear error |

The Terms-of-Service checkboxes on the portal page are a browser-side gate.
The authentication JSON has no terms field, so there is nothing to click in an
automated request. The tool does not bypass authentication: it sends the same
credentials you would type.

## Configuration

`init` writes a private (`0600`) JSON file to the OS config directory:

| OS | Path |
|---|---|
| macOS | `~/Library/Application Support/omada-autologin/config.json` |
| Linux | `~/.config/omada-autologin/config.json` |
| Windows | `%AppData%\omada-autologin\config.json` |

Example:

```json
{
  "username": "",
  "password": "your-password",
  "username_rotation": {
    "enabled": true,
    "digits": 3,
    "min": 1,
    "max": 112
  },
  "portal_url": "",
  "portal_hosts": ["192.168.1.3"],
  "check_urls": [
    "http://example.com/",
    "http://connectivitycheck.gstatic.com/generate_204",
    "http://captive.apple.com/hotspot-detect.html"
  ],
  "interval_seconds": 60,
  "retry_min_seconds": 5,
  "retry_max_seconds": 60,
  "timeout_seconds": 8
}
```

| Field | Meaning |
|---|---|
| `username` | Optional when rotation is enabled. Leave it empty to use the random suffix as the whole username. If set, its last `digits` characters must be digits and are replaced; the prefix is kept. |
| `password` | Shared portal password. Required. |
| `username_rotation` | New configs enable it. On each login a number from `min` to `max` replaces the trailing `digits` characters of `username`, or becomes the whole username when `username` is empty. Set `enabled` to `false` to keep one fixed username. |
| `portal_url` | Optional seed URL used when the gateway's logout page cannot be parsed. The last successful URL is also remembered in `state.json`. |
| `portal_hosts` | Allow-list of controller hosts. Credentials are **only** sent to these hosts. Add the IP/hostname of your controller. |
| `check_urls` | Plain-HTTP URLs intercepted by the captive portal. HTTPS checks are bypassed by most gateways and are less useful. |
| `interval_seconds` | Online health-check interval. Minimum 5, default 60. |
| `retry_min_seconds` | First retry delay while offline/rejected. Minimum 2, default 5. |
| `retry_max_seconds` | Retry delay cap, 5–3600, default 60. |
| `timeout_seconds` | Per-request timeout, 1–120, default 8. |

### Randomized usernames

New configs need no username at all. On each login the tool sends a random
three-digit username from `001`–`112` with the shared password:

```sh
omada-autologin init --password 'your-password'
```

The generated suffix is the complete username; the tool never asks for a
registration number. `status` shows:

```
user:   (generated)
rotate: random last 3 digits (001-112)
```

If the portal usernames have a fixed prefix, such as `24105157001` through
`24105157112`, pass that prefix as `--username`. Only the last three digits
change:

```sh
omada-autologin init --username 24105157001 --password 'your-password'
```

Each login sends one of `24105157001`–`24105157112`: the last three digits are
replaced and the prefix is kept. Within a run, the order is shuffled so every
suffix is used once before any is reused, and two consecutive attempts never
pick the same suffix. A daemon restart starts a new shuffle.

Use a different range in one step:

```sh
omada-autologin init --password 'your-password' --random-range 020-112
```

Or update an existing config without retyping the password:

```sh
omada-autologin randomize --range 020-112
omada-autologin randomize              # restore the default 001-112
omada-autologin randomize --off        # keep one fixed username (must already be set)
```

`init --fixed --username NAME` creates a fixed-username config. When a prefix is
used, the last `digits` characters of `username` must already be digits; only
that suffix changes.

The range is inclusive and bounded; the tool never sends a username outside it.
Enable rotation only when every username in the range belongs to you or is a
shared account you are authorised to use. The tool does not create the
accounts: create the matching usernames in Omada with the same password.

Use a different config file with `--config PATH` on any command.

`install --interval N` **writes** `N` to `interval_seconds` and then installs
the service. Editing the config later only needs a reload:

```sh
systemctl --user reload omada-autologin   # Linux, sends SIGHUP
launchctl kickstart -k gui/$(id -u)/com.omada.autologin   # macOS, restarts
# Windows: `omada-autologin install` again, or restart the task
```

## Commands

| Command | Purpose |
|---|---|
| `setup` | Explicit form of the no-argument command: create the config if needed, test the portal, then install and start the daemon. `--password`, `--host`, `--username`, `--interval`, `--no-login`, `--no-service`, `--force`. |
| `init` | Create config only. Omit `--username` to generate a random 3-digit username (001–112); pass it to keep a fixed prefix and rotate only the suffix. Flags: `--username`, `--password`, `--host`, `--portal-url`, `--random-range`, `--rotate-last`, `--rotate-min`, `--rotate-max`, `--fixed`, `--force`. |
| `once` | One check: log in only if the network is captive. `--force` logs in even when online; `--dry-run` prints the request without sending credentials; `--url` uses a specific portal URL. |
| `login` | Same as `once --force`. |
| `run` | Run the daemon in the foreground (debugging or your own supervisor). `--interval` temporarily overrides config. |
| `status` | Show online state, discovered/cached portal URL, username rotation, config and log paths, failure count. `--json` for machines. |
| `discover` | Print the portal entry URL, if one is visible. |
| `randomize` | Enable or change username rotation on an existing config: `--range 001-112` (default) or `--off`. |
| `install` | Install the native daemon service. `--interval` persists a new interval to config first. |
| `uninstall` | Remove the background service. The config is kept. |
| `version` | Print version. |

Common flags: `--config PATH`, `--verbose` (also log to stderr).

Exit codes: `0` success or nothing to do, `1` failure, `2` bad usage.

## CachyOS and Hyprland

The binary is statically linked, so CachyOS/Arch needs no extra packages, no
AUR step, and no Python or Node runtime. The installer detects CachyOS/Arch and
Hyprland and installs a **systemd user service** (`omada-autologin.service`)
with `Restart=always`, `RestartSec=5`, and `ExecReload` wired to `SIGHUP`.

If Hyprland is started inside a normal systemd user session (`uwsm`, a display
manager, or `exec Hyprland` after a TTY login), everything works as-is:

```sh
systemctl --user status omada-autologin
journalctl --user -u omada-autologin -f
systemctl --user restart omada-autologin
systemctl --user reload omada-autologin    # re-read config without dropping state
```

To keep the daemon running while you are logged out, enable lingering once:

```sh
loginctl enable-linger "$USER"
```

If you start Hyprland in a way that has no `systemd --user` manager, `install`
prints a warning instead of leaving you with a dead service. Start the daemon
from Hyprland instead:

```conf
# ~/.config/hypr/hyprland.conf
exec-once = ~/.local/bin/omada-autologin run
```

Hyprland does not need a network dispatcher script: the daemon watches netlink
directly, so reconnects, `nmcli` changes, and roaming between APs all wake it.
Check the log with:

```sh
tail -f ~/.cache/omada-autologin/omada-autologin.log
```

## Platform notes

### macOS

`install` writes `~/Library/LaunchAgents/com.omada.autologin.plist` and loads it
into your GUI session. The plist uses `RunAtLoad`, `KeepAlive` for unsuccessful
exits, `ThrottleInterval` 10 s, and `ProcessType Background`. It follows config
changes on restart:

```sh
launchctl kickstart -k gui/$(id -u)/com.omada.autologin
```

Inspect it with:

```sh
launchctl print gui/$(id -u)/com.omada.autologin
```

Do not run `install` with `sudo`. If you previously used a different
auto-login agent for the same portal, unload it first so two clients do not
fight over the session.

### Linux

`install` writes `~/.config/systemd/user/omada-autologin.service` and enables
it. It requires a working `systemd --user` session. There is no timer unit; the
daemon itself handles scheduling. A system-wide install is not supported
deliberately: the service should run as the user whose Wi-Fi session it
maintains.

The unit includes `Nice=10`, `NoNewPrivileges=true`, and `PrivateTmp=true`. It
does not need root, `CAP_NET_ADMIN`, or any elevated capability.

### Windows

`install` creates a Task Scheduler task named **Omada Autologin** that runs at
logon:

```text
"C:\path\to\omada-autologin.exe" run --config "C:\Users\you\AppData\Roaming\omada-autologin\config.json"
```

The task uses `MultipleInstancesPolicy IgnoreNew`, has no execution time limit,
and restarts on failure. It runs as your interactive user, so it uses the same
Wi-Fi session as your desktop. Inspect it with:

```powershell
schtasks /Query /TN "Omada Autologin" /V /FO LIST
```

If Task Scheduler accepts the logon task but rejects the richer XML schema on
an older Windows build, the installer falls back to a plain `ONLOGON` task that
still starts the same daemon.

## Logs and state

Logs and state live in the OS cache directory:

| OS | Path |
|---|---|
| macOS | `~/Library/Caches/omada-autologin/` |
| Linux | `~/.cache/omada-autologin/` |
| Windows | `%LocalAppData%\omada-autologin\` |

Files:

- `omada-autologin.log` — login history and errors (rotated at 512 KiB)
- `service.log` — stdout/stderr from launchd on macOS
- `state.json` — failure count, last attempt/success, cached portal URL
- `lock` — prevents overlapping runs (stale after 10 minutes)

```sh
tail -f ~/Library/Caches/omada-autologin/omada-autologin.log   # macOS
tail -f ~/.cache/omada-autologin/omada-autologin.log           # Linux
journalctl --user -u omada-autologin -f                        # Linux service
```

## Troubleshooting

**`not configured`** — run `omada-autologin init` or pass `--config`.

**`refusing portal host`** — the redirect points at a host that is not in
`portal_hosts`. Add the controller's IP or hostname. This guard exists so
credentials can never be sent to an unrelated host.

**`errorCode -41525`** — wrong password.

**`errorCode -41526`** — the controller considers the client stale. Toggle
Wi-Fi off/on, copy the fresh portal URL, and run
`omada-autologin login --url '<url>'`.

**`errorCode -41527`** — unknown local user. With rotation enabled, every
username in the configured range must exist on the controller and use the
configured password. Create the missing users or narrow the range with
`omada-autologin randomize --range 001-050`.

**Login accepted, internet not confirmed** — usually a data/time quota. The
tool keeps checking with backoff and retries when the portal resets the quota.
It cannot create quota.

**Controller IP changed** — update `portal_hosts` in the config, then
`systemctl --user reload omada-autologin` (Linux). On macOS or Windows, restart
the service/task. If you changed the interval too, `install --interval N`
persists it.

**Linux: `systemd user session is not available`** — you are probably in a
container, over SSH without a user manager, or `systemd` is not PID 1. Use the
Hyprland `exec-once` fallback above, or run `omada-autologin run` under your
own supervisor.

**Windows: task exists but does nothing** — open Task Scheduler, find
**Omada Autologin**, and check "Last Run Result". Running
`omada-autologin once --verbose` in the same user account shows the error.

## Uninstall

```sh
omada-autologin uninstall
```

Then remove the binary and, if you want a clean slate, the config and cache:

```sh
# Linux
rm -f ~/.local/bin/omada-autologin
rm -rf ~/.config/omada-autologin ~/.cache/omada-autologin

# macOS
rm -f ~/.local/bin/omada-autologin
rm -rf ~/Library/Application\ Support/omada-autologin
rm -rf ~/Library/Caches/omada-autologin
```

Windows:

```powershell
Remove-Item "$env:LOCALAPPDATA\omada-autologin" -Recurse -Force
Remove-Item "$env:APPDATA\omada-autologin" -Recurse -Force
# Optionally remove the bin directory from your user PATH.
```

## Security

- The config is written with mode `0600`; the containing directory is `0700`.
- Credentials are sent only to hosts in `portal_hosts`.
- Username rotation is bounded to the configured numeric range; it never sends
  a username outside that range. Use it only for accounts you administer.
- The portal's self-signed HTTPS certificate is accepted **only** for portal
  requests. Connectivity probes use normal certificate verification.
- No telemetry, cloud service, or third-party API is involved.
- The password is stored in plain text in the config, because the portal
  requires the plain password. Use full-disk encryption and keep the file
  private.
- The installer can take credentials from environment variables for unattended
  setup; those variables may be visible in the process environment. For
  interactive use, omit them and let `init` prompt. `--password` on the command
  line is also briefly visible in the process list, so prefer the prompt or the
  environment form on a machine you control.

## Development

```sh
go test ./...
go vet ./...
go build ./...
```

Cross-compile:

```sh
GOOS=windows GOARCH=amd64 go build -o omada-autologin.exe .
GOOS=linux   GOARCH=arm64 go build -o omada-autologin-linux-arm64 .
```

Releases are built by GitHub Actions for Linux, macOS and Windows on `amd64`
and `arm64` when a `v*` tag is pushed. Each archive contains the stable
`omada-autologin` binary, this README, and the LICENSE; `SHA256SUMS.txt` covers
every archive.

## License

MIT — see [LICENSE](LICENSE).
