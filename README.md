# omada-autologin

Keep an [TP-Link Omada](https://www.tp-link.com/en/omada-sdn/) captive-portal
session alive on **macOS, Linux and Windows**.

Campus and guest Wi-Fi networks built on Omada often log a client out when a
data quota resets or when the controller ages the session. This tool polls a
plain-HTTP connectivity URL, discovers the Omada portal entry URL that the
gateway redirects to, and re-authenticates with **your own** username and
password. It runs as a native per-user service:

| Platform | Scheduler | Default interval |
|---|---|---|
| macOS | `launchd` LaunchAgent | 15 s |
| Linux | `systemd` user timer | 15 s |
| Windows | Task Scheduler | 1 min (OS minimum) |

> **One account only.** This tool authenticates with the single account in
> your config. It deliberately has no username-range or rotation feature:
> using another student's registration number would be unauthorised access to
> their account and would consume their quota. Only use it on a network where
> you are permitted to connect.

## Install

### Prebuilt binaries

Download the archive for your OS and CPU from
[Releases](https://github.com/pritkr/omada-autologin/releases), unpack it, and
put `omada-autologin` somewhere on your `PATH`.

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

```sh
# 1. Create a config. Prompts for username/password; --host is the Omada
#    controller/gateway that serves the portal.
omada-autologin init --host 192.168.1.3

# 2. See what the tool discovered, without sending credentials.
omada-autologin once --dry-run

# 3. Log in once.
omada-autologin once

# 4. Install the background service.
omada-autologin install
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
  "username": "YOUR_REGISTRATION_NUMBER",
  "password": "your-password",
  "portal_url": "",
  "portal_hosts": ["192.168.1.3"],
  "check_urls": [
    "http://example.com/",
    "http://connectivitycheck.gstatic.com/generate_204",
    "http://captive.apple.com/hotspot-detect.html"
  ],
  "interval_seconds": 15,
  "timeout_seconds": 8
}
```

| Field | Meaning |
|---|---|
| `username` / `password` | Your own portal credentials. |
| `portal_url` | Optional seed URL used when the gateway's logout page cannot be parsed. The last successful URL is also remembered in `state.json`. |
| `portal_hosts` | Allow-list of controller hosts. Credentials are **only** sent to these hosts. Add the IP/hostname of your controller. |
| `check_urls` | Plain-HTTP URLs intercepted by the captive portal. HTTPS checks are bypassed by most gateways and are less useful. |
| `interval_seconds` | Poll interval for `run` and for the installed timer. Minimum 5. On Windows the scheduled task runs every minute. |
| `timeout_seconds` | Per-request timeout. |

Use a different config file with `--config PATH` on any command.

## Commands

| Command | Purpose |
|---|---|
| `init` | Create config. Flags: `--username`, `--password`, `--host`, `--portal-url`, `--force`. |
| `once` | One check: log in only if the network is captive. `--force` logs in even when online; `--dry-run` prints the request without sending credentials; `--url` uses a specific portal URL. |
| `login` | Same as `once --force`. |
| `run` | Foreground loop (for debugging or your own supervisor). `--interval` overrides config. |
| `status` | Show online state, discovered/cached portal URL, config and log paths, failure count. `--json` for machines. |
| `discover` | Print the portal entry URL, if one is visible. |
| `install` | Install the native background service. `--interval` overrides config. |
| `uninstall` | Remove the background service. |
| `version` | Print version. |

Common flags: `--config PATH`, `--verbose` (also log to stderr).

Exit codes: `0` success or nothing to do, `1` failure, `2` bad usage.

## What it actually sends

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

### Discovery and fallback

1. Request each `check_url`.
2. Look for a captive-portal redirect in `Location`.
3. If the gateway answers `200` in place, search the body for an Omada
   `/portal/entry?...` URL in JavaScript, a meta refresh, or a plain link.
   Escaped slashes (`https:\/\/...`) and `&amp;` entities are normalised.
4. If no URL can be parsed, reuse `portal_url` / the last successful URL from
   state. Many controllers keep accepting a recent entry URL.
5. Validate the host against `portal_hosts`, extract `cid` (client MAC), `ap`
   or `gateway` MAC, `ssid`, and `rid`, then authenticate.

A login is only reported as successful after a follow-up probe confirms that
the internet is actually reachable. If the controller accepts the login but
the quota is exhausted, the tool logs that distinction and retries later with
a five-minute backoff after five consecutive failures.

## Logs and state

Logs and state live in the OS cache directory:

| OS | Path |
|---|---|
| macOS | `~/Library/Caches/omada-autologin/` |
| Linux | `~/.cache/omada-autologin/` |
| Windows | `%LocalAppData%\omada-autologin\` |

Files:

- `omada-autologin.log` — login history and errors (rotated at 512 KiB)
- `service.log` — stdout/stderr from the scheduler on macOS
- `state.json` — failure count, last attempt/success, cached portal URL
- `lock` — prevents overlapping runs (stale after 10 minutes)

```sh
tail -f ~/Library/Caches/omada-autologin/omada-autologin.log   # macOS
tail -f ~/.cache/omada-autologin/omada-autologin.log           # Linux
```

## Platform notes

### macOS

`install` writes `~/Library/LaunchAgents/com.omada.autologin.plist` and loads
it into your GUI session. It runs at login and every `interval_seconds`.
Do not run `install` with `sudo`.

### Linux

`install` writes `~/.config/systemd/user/omada-autologin.{service,timer}` and
enables the timer. It requires a working `systemd --user` session.

To keep it running while you are logged out, enable lingering once:

```sh
loginctl enable-linger "$USER"
```

A system-wide install is not supported deliberately: the service should run
as the user whose Wi-Fi session it maintains.

### Windows

`install` creates a Task Scheduler task named **Omada Autologin** that runs

```text
"C:\path\to\omada-autologin.exe" once --config "C:\Users\you\AppData\Roaming\omada-autologin\config.json"
```

every minute while you are logged on. Task Scheduler does not support
sub-minute repetition. To run it without an interactive login, use Task
Scheduler's "Run whether user is logged on or not" option and store the
password for the task.

## Troubleshooting

**`not configured`** — run `omada-autologin init` or pass `--config`.

**`refusing portal host`** — the redirect points at a host that is not in
`portal_hosts`. Add the controller's IP or hostname. This guard exists so
credentials can never be sent to an unrelated host.

**`errorCode -41525`** — wrong password.

**`errorCode -41526`** — the controller considers the client stale. Toggle
Wi-Fi off/on, copy the fresh portal URL, and run
`omada-autologin login --url '<url>'`.

**`errorCode -41527`** — unknown local user.

**Login accepted, internet not confirmed** — usually a data/time quota. The
tool backs off for five minutes after five failures and tries again when the
portal resets the quota. It cannot create quota.

**Controller IP changed** — update `portal_hosts` in the config and run
`omada-autologin install` again if you also changed the interval.

**Linux: `systemd user session is not available`** — you are probably in a
container, over SSH without a user manager, or `systemd` is not PID 1. Run
`omada-autologin run` under your own supervisor instead.

**Windows: task exists but does nothing** — open Task Scheduler, find
**Omada Autologin**, and check "Last Run Result". Running
`omada-autologin once --verbose` in the same user account shows the error.

## Uninstall

```sh
omada-autologin uninstall
rm -rf ~/Library/Application\ Support/omada-autologin   # macOS config
rm -rf ~/Library/Caches/omada-autologin                 # macOS state
```

Linux: `~/.config/omada-autologin` and `~/.cache/omada-autologin`.
Windows: `%AppData%\omada-autologin` and `%LocalAppData%\omada-autologin`.

## Security

- The config is written with mode `0600`; the containing directory is `0700`.
- Credentials are sent only to hosts in `portal_hosts`.
- The portal's self-signed HTTPS certificate is accepted **only** for portal
  requests. Connectivity probes use normal certificate verification.
- No telemetry, cloud service, or third-party API is involved.
- The password is stored in plain text in the config, because the portal
  requires the plain password. Use full-disk encryption and keep the file
  private. Environment variables are not used to avoid leaking secrets into
  process listings.

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
and `arm64` when a `v*` tag is pushed.

## License

MIT — see [LICENSE](LICENSE).
