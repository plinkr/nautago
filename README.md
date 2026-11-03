# NautaGo

English | [Spanish](README_ES.md)

Client and command-line tool for the ETECSA Nauta captive portal, written in Go.

NautaGo is a rewrite of [NautaPy](https://github.com/plinkr/nautapy). It keeps
the same commands, flags and observable behavior, and ships as a single static
binary with no runtime dependencies. The CLI output is in Spanish, as in NautaPy.

## Features

- Log in to and out of the Nauta portal, with the session persisted on disk so
  `nauta down` can close a session opened by another process.
- Multiple stored accounts with a default user and prefix matching
  (`nauta up plinkr` finds `plinkr@nauta.com.cu`).
- Passwords encrypted at rest with AES-GCM and a local 256-bit key.
- Connection history with current-month, previous-month and full listings, plus
  monthly totals per user.
- Remaining time and account credit queries.
- `run-connected`: run a command with a session open and close it afterwards.
- On-demand HTTP/HTTPS proxy that shares one session between concurrent
  clients and closes it on a billing-unit boundary.
- Detects OpenVPN and ProtonVPN, which take over the routing table, and stops
  them before talking to the portal.
- One-time import of NautaPy users, default user, history and open session.

## Requirements

- Go 1.26 or later to build.
- No cgo and no system libraries. SQLite is provided by a pure-Go driver.
- Linux, macOS or Windows.
- Optional: `sudo` and `killall` (Unix) or `taskkill` (Windows) to stop
  OpenVPN; `nmcli` and the `protonvpn` CLI to detect and stop ProtonVPN.

## Installation

Every [GitHub release](https://github.com/plinkr/nautago/releases) ships
prebuilt binaries:

| OS      | Archive suffix                                                      |
|---------|---------------------------------------------------------------------|
| Linux   | `linux_amd64`, `linux_386`, `linux_arm64`, `linux_armv7`, `linux_armv6` |
| Windows | `windows_amd64`, `windows_386`, `windows_arm64` (`.zip`)            |
| macOS   | `darwin_arm64` (Apple Silicon), `darwin_amd64` (Intel)              |

On a Raspberry Pi use `linux_arm64` with a 64-bit OS, `linux_armv7` for
Pi 2/3/4 with a 32-bit OS and `linux_armv6` for Pi Zero/1. Verify downloads
against `checksums.txt`, or with `gh attestation verify <archive> -R plinkr/nautago`.

With Go:

```sh
go install github.com/plinkr/nautago/cmd/nauta@latest
```

From a checkout:

```sh
git clone https://github.com/plinkr/nautago.git
cd nautago
go build -o nauta ./cmd/nauta
```

## Quickstart

```sh
nauta users add plinkr@nauta.com.cu   # prompts for the password
nauta up                                 # log in; Ctrl+C logs out
```

From another terminal:

```sh
nauta down
```

## Usage

```text
nauta [-h] [-d] [-lc] [-lm] [-ac] [-rc] [--version] COMMAND ...
```

| Command | Description |
| --- | --- |
| `up` | Open a session and keep it open until interrupted. |
| `down` | Close the active session. |
| `info` | Show the account and its remaining time. |
| `is-online` | Report whether there is Internet access. |
| `is-logged-in` | Report whether a Nauta session is open. |
| `run-connected` | Run a command with a session open, then log out. |
| `users` | Manage stored accounts. |
| `proxy` | Run a proxy that opens a session on demand. |

All commands except `run-connected` accept `-h`. Flags may appear before or
after positional arguments, so `nauta up plinkr --no-log` works.

### up

```text
nauta up [-t TIME] [-b] [-nl] [user] [password]
```

| Flag | Description |
| --- | --- |
| `-t`, `--session-time` | Log out after this long. Seconds by default; accepts `s`, `m` and `h` suffixes (`90`, `90s`, `30m`, `2h`). |
| `-b`, `--batch` | Log in, print the remaining time and exit. The session stays open until `nauta down`. |
| `-nl`, `--no-log` | Do not record this connection in the history. |

Without a user, the default user is used; if none is set, the first stored one.

```sh
nauta up plinkr
nauta up -t 1h
nauta up -b plinkr
```

Without `-b`, the elapsed connection time is shown in the terminal. The session
is closed with `Ctrl+C` or with `nauta down` from another terminal.

### down, is-online, is-logged-in

```sh
nauta down
nauta is-online       # Online: No
nauta is-logged-in    # Sesión activa: No
```

`is-online` is the only command that never stops a VPN, because it does not talk
to the portal. With a VPN connected, a positive answer means the Internet is
reachable through the VPN.

### info

```text
nauta info [-c] [user] [password]
```

```text
$ nauta info plinkr
Usuario Nauta: plinkr@nauta.com.cu
Tiempo restante: 02:14:24
```

`-c`, `--credit` also prints the account credit:

```text
$ nauta info --credit
Usuario Nauta: plinkr@nauta.com.cu
Tiempo restante: 02:14:24
Crédito: 1.12 CUC
```

The portal only serves the credit while no session is open, so while logged in
the error message is shown in its place.

### run-connected

```text
nauta run-connected [-u user] [-p password] command [args...]
```

```sh
nauta run-connected -u plinkr rsync -av src/ host:dst/
```

The command is executed directly, without a shell, so globs and pipes are not
expanded. `nauta` exits with the command's exit code, or 127 if it could not be
started. The session is closed even when the command fails or is interrupted.
Prefer a stored password over `-p`: command-line arguments are visible to other
local users.

### users

```text
nauta users add user [password]
nauta users set-default user
nauta users set-password user [password]
nauta users remove user
nauta users list
```

The password is read from the terminal without echo when omitted. When stdin is
not a terminal, one line is read, so it can be piped in. Removing the default
user also clears the default mark, and later commands use the first remaining
user.

### Connection history

| Flag | Description |
| --- | --- |
| `-lc`, `--list-conn` | List connections for the current month. |
| `-lm`, `--last-month` | With `-lc`: list the previous month instead. |
| `-ac`, `--all-conn` | With `-lc`: list every connection. |
| `-rc`, `--resume-conn` | Print total hours per user and month. |
| `-d`, `--debug` | Print debug information to stderr. |

```sh
nauta -lc
nauta -lc -lm
nauta -rc -lc -ac
```

`-lm` and `-ac` without `-lc` are errors. `up --batch` and `run-connected` do
not record history, as in NautaPy.

### proxy

```text
nauta proxy -t UNIT [-p PORT] [--host ADDR] [-u USER] [-m MAX] [-l LOG] [--allow LIST] [--deny LIST]
```

| Flag | Default | Description |
| --- | --- | --- |
| `-t`, `--time-unit` | required | Minimum billing unit. Same format as `up -t`. |
| `-p`, `--port` | `3128` | Listen port. |
| `--host` | `127.0.0.1` | Listen address. |
| `-u`, `--user` | default user | Account to log in with. |
| `-m`, `--max-conn` | unlimited | Maximum concurrent connections. |
| `-l`, `--log` | `-` (stdout) | Log file. |
| `--allow` | none | Comma-separated domains to allow. Subdomains match. |
| `--deny` | none | Comma-separated domains to block. Checked before `--allow`. |

```sh
nauta proxy -t 120s
export http_proxy=http://127.0.0.1:3128
export https_proxy=http://127.0.0.1:3128
```

The proxy does not authenticate clients. Binding to `0.0.0.0` lets anyone who
can reach the port browse on your account. Sessions opened by the proxy are
recorded in the history.

## Configuration

| Variable | Description |
| --- | --- |
| `NAUTAGO_HOME` | Data directory. Overrides everything else. |
| `XDG_DATA_HOME` | If set, data goes to `$XDG_DATA_HOME/nautago`. |

Default data directory: `~/.local/share/nautago` on Unix and
`%USERPROFILE%\AppData\Roaming\nautago` on Windows. The directory is created
with mode `0700` and every file with `0600`.

| File | Contents |
| --- | --- |
| `users.db` | Stored accounts and default user (SQLite). |
| `connections.db` | Connection history (SQLite). |
| `credentials.key` | Key used to encrypt stored passwords. |
| `nauta-session` | Tokens of the open portal session (JSON). |

If `credentials.key` is lost, stored passwords cannot be recovered. Commands
that do not need them, such as `nauta down`, keep working. Re-enter each
password with `nauta users set-password`, which is the only command that
creates a new key in that situation.

## How it works

### Session lifecycle

`up` first probes for Internet access. If the network is already online it
refuses to log in; if the portal intercepts the probe, it logs in and writes
`nauta-session` atomically. A stale session file from a session the portal no
longer knows does not block a new login, and is only replaced once the new
login succeeds. If the login fails, the file is kept so that `nauta down` can
still close a session that is open in another terminal.

`down` sends the stored tokens to the portal. On network failures the logout is
attempted up to five times. The session file is removed only after the portal
confirms the logout: those tokens are the only way to close a session that is
still billing, so a failed logout leaves them in place and `nauta down` can be
run again. The history entry is closed either way, because the attempt
happened.

### VPN handling

OpenVPN and ProtonVPN take over the routing table, so neither the portal nor
the Internet probe answers through the Nauta network. Before any portal request
(logout, remaining time, credit) NautaGo stops the VPN and says so.

Before logging in with a VPN up, `up`, `run-connected` and `proxy` check the
probe:

- Internet reachable: the VPN is providing it. The login is refused with an
  explanation, and the VPN is left running, because cutting off someone who is
  browsing is worse than refusing.
- Nothing reachable: most likely a stale tunnel is holding the routes. The VPN
  is stopped and the login proceeds.

The probe tries more than one host, because a Cuban site may be unreachable
from a VPN exit and that must not be mistaken for a dead network.

### Proxy billing

The first request opens a session; concurrent requests share it. When the last
connection finishes, the proxy keeps the session open until the next multiple
of `--time-unit` since login, because that fraction is already paid for, and
then logs out. Requests arriving in that window reuse the session. A session
the proxy did not open, such as one from `nauta up`, is never closed by it. If
the proxy is stopped before the boundary, the session stays open and
`nauta down` closes it.

## Migrating from NautaPy

On every start, if `~/.local/share/nautapy` exists and NautaGo has no data yet,
NautaGo imports the users, default user and connection history. NautaPy's
files are opened read-only and never modified, so NautaPy keeps working.

- The import is recorded in NautaGo's own database and runs once. Deleting
  your last account does not trigger a re-import.
- If NautaGo already has data, nothing is imported and no mark is written. A
  notice is printed, and the import is retried once NautaGo's data is moved
  away.
- Rows with unreadable dates are skipped and the count is reported.
- Passwords are decoded from base85 and re-encrypted with the local key.
- Timestamps stored in UTC are converted to local time.
- Connections made with NautaPy after the import do not appear in NautaGo.

An open NautaPy session (`~/.local/share/nautapy/nauta-session`) is moved, not
copied, into NautaGo's data directory when NautaGo has none, so that
`nauta down` can close it. A session can only have one owner; a copy left
behind would be re-imported after every logout. A `nautapy up` still running
in another terminal will stop seeing the session.

## Differences from NautaPy

- Failing commands exit with status 1. NautaPy always exited with 0.
- `run-connected` runs the command without a shell and returns its exit code.
- OpenVPN is stopped with `sudo killall openvpn` (Unix) or `taskkill`
  (Windows) instead of a hardcoded script path, and NautaGo waits for the
  process to exit.
- The VPN is stopped before every portal request, not only before logout.
  NautaPy's final "remaining time" query failed with a VPN up.
- `up` distinguishes "Internet through the VPN" from "nothing reachable" when a
  VPN is connected (see VPN handling).
- A failed logout keeps the session file.
- A session file that cannot be parsed is not treated as an open session.
- A failure writing the history is reported on stderr but does not change the
  exit code.
- `nauta down` prints the user being disconnected.
- Passwords are encrypted instead of base85-encoded.
- The proxy is a new implementation. NautaPy only described it in
  `PROXY_GUIDE.md`. Domain filtering and the loopback default were added.

## Development

```sh
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
```

Protocol tests use `net/http/httptest` with portal pages in
`internal/nauta/testdata`, some captured and some written to match them (see
its README). No test contacts the ETECSA service.

Dependencies:

| Module | Purpose |
| --- | --- |
| `golang.org/x/net` | HTML parser. The portal is a web application, not an API. |
| `golang.org/x/term` | Reading passwords without echo. |
| `modernc.org/sqlite` | Pure-Go SQLite, compatible with NautaPy's database files. |

The SQLite driver accounts for most of the binary size: about 18 MB for a plain
`go build` and 12 MB for release builds, which strip symbols with `-s -w`.

Pushing a tag publishes a release: `.github/workflows/release.yml` runs the
checks above and then GoReleaser with `.goreleaser.yaml`. Tags with a suffix,
such as `v0.9.0-rc.1`, are published as pre-releases.

```sh
git tag -a v0.9.0 -m "v0.9.0"
git push origin v0.9.0
```

The version comes from the tag only. `nauta --version` prints it from the
release ldflags or, for `go install` and `go build`, from the module version Go
stamps into the binary.

## Project layout

```text
cmd/nauta/          entry point: signals, storage and CLI wiring
internal/nauta/     portal protocol, session file and client lifecycle
internal/storage/   users, credentials, history and NautaPy import
internal/cli/       argument parsing, commands and output
internal/proxy/     proxy with a shared session and billing-unit hold
internal/platform/  OS-specific process and VPN handling
```

## License

MIT. See [LICENSE.md](LICENSE.md).
