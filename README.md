# Khelam.gg

Self-hosted tunnels for game servers and other services behind NAT, in
the spirit of playit.gg and Cloudflare Tunnel. Run `khelam-server` on a
machine with a public IP, run `khelam-agent` next to your service, and
players connect to the server's public port.

- **TCP and UDP** tunnels, several per agent
- **Sticky ports**: each tunnel keeps its public port across restarts
- **QUIC** between agent and server, with UDP carried as datagrams for low latency
- **Per-agent tokens**, stored hashed on the server and revocable without a restart
- Optional **PROXY protocol v2**, so TCP services see real client addresses

```
player ──TCP/UDP──▶ khelam-server :41000 ══QUIC══▶ khelam-agent ──▶ localhost:25565
                    (public IP)                    (behind NAT)
```

## Quick start

On the public machine, create a token and start the server:

```sh
khelam-server gen-token my-pc         # prints a token and a tokens.yaml entry
$EDITOR tokens.yaml                   # paste the entry under "tokens:"
khelam-server -tokens-file tokens.yaml
```

The server logs its certificate fingerprint on startup:

```
{"level":"INFO","msg":"server accepting agents","address":"[::]:31000","fingerprint":"sha256:2673…"}
```

On the machine running your service, start an agent with that token
and fingerprint:

```sh
export KHELAM_TOKEN=khelam_…
khelam-agent -server my.server.example:31000 \
  -fingerprint sha256:2673… \
  -tunnel minecraft=localhost:25565 \
  -tunnel bedrock=udp://localhost:19132
```

The agent logs each tunnel's public port. Players connect to
`my.server.example:<publicPort>`.

Open UDP 31000 (agent connections) and the public port range (41000–41999
by default, TCP and UDP) in the server's firewall.

## Agent

### Terminal interface

Run in a terminal, the agent shows a live view instead of JSON logs:

```
 khelam agent v0.2.0 · tunnel.example.com:31000 · ● connected 2h13m
 ───────────────────────────────────────────────────────────────────────────
  TUNNEL      PROTO   PUBLIC ADDRESS             LOCAL             CONNS        IN       OUT
▸ minecraft   tcp     tunnel.example.com:41000   localhost:25565       3    1.2 MB   48.0 MB
  bedrock     udp     tunnel.example.com:41000   localhost:19132       5  310.0 kB    2.1 MB
 ───────────────────────────────────────────────────────────────────────────
 21:40:12 DBG connection opened tunnel=minecraft clientAddr=203.0.113.7:51234
 ───────────────────────────────────────────────────────────────────────────
 ↑/↓ select · c copy address · l hide log · q quit
```

`c` copies the selected tunnel's public address to the clipboard (also
over SSH, in terminals that support OSC 52). While reconnecting, `r`
retries immediately. `-tui auto|on|off` controls it. The default,
`auto`, uses it only when stdin and stdout are a terminal, so services,
containers and pipes keep JSON logs.

### Tunnels

`-tunnel [NAME=][PROTO://]HOST:PORT`, repeatable:

| Spec | Meaning |
|---|---|
| `localhost:8080` | TCP tunnel named `tcp-8080` |
| `web=localhost:8080` | TCP tunnel named `web` |
| `game=udp://localhost:19132` | UDP tunnel named `game` |

The name keys the tunnel's sticky port, so keep it stable.

### Config file

Everything can also go in a YAML file (see
[`examples/agent.yaml`](examples/agent.yaml)):

```sh
khelam-agent -config agent.yaml
```

Flags given on the command line override the file. `-tunnel` flags
replace the file's tunnels. Relative paths in the file are resolved
against the file's directory.

### Trusting the server

QUIC always uses TLS. The agent verifies the server in one of three ways:

- `-fingerprint sha256:…` pins the server's self-signed certificate (the default setup)
- `-tls-ca ca.pem` trusts certificates signed by your own CA
- neither: the system's trusted CAs, for a server given `-tls-cert`/`-tls-key`

### Token

From `-token`, `-token-file`, `token`/`token_file` in the config file, or
the `KHELAM_TOKEN` environment variable. Prefer a file or the
environment, which keep the token out of the process list.

### Exit status

The agent reconnects by itself, with backoff, after network failures. It
only exits on errors that a retry cannot fix (bad config, rejected token,
untrusted server, or another agent taking over its ID), with **status 3**.

## Server

| Flag | Default | |
|---|---|---|
| `-listen` | `0.0.0.0:31000` | UDP address for agent connections |
| `-port-range` | `41000-41999` | Public ports, inclusive. TCP and UDP are allocated separately. |
| `-tokens-file` | | Per-agent tokens. Reloaded on `SIGHUP`. |
| `-token` | | A single shared token instead of `-tokens-file` (also `$KHELAM_TOKEN`) |
| `-state-dir` | `~/.config/khelam/server` | Self-signed certificate and port reservations |
| `-tls-cert`, `-tls-key` | | Use this certificate instead of a self-signed one |
| `-max-conns-per-tunnel` | `1024` | Concurrent TCP connections or UDP clients per tunnel (`0` = unlimited) |

### Tokens

`khelam-server gen-token NAME` prints a new token and the entry for the
tokens file (see [`examples/tokens.yaml`](examples/tokens.yaml)). The
file holds only SHA-256 hashes. After editing it, send the server
`SIGHUP`. Agents whose token was removed are disconnected.

Agents are scoped to their token. An agent can never take over, or
receive the ports of, an agent registered under a different token. With
no tokens file and no `-token`, any agent may register, and the server
warns about it.

### Sticky ports

The server remembers each tunnel's port per agent in
`<state-dir>/ports.json`. A reconnecting agent gets the same ports back,
and other agents are given unreserved ports first. Reserved ports are
only reused when nothing else is free, so stickiness never costs
capacity.

Each agent install has a random ID, kept in `-id-file`
(`~/.config/khelam/agent/agent-id`). If a second agent connects with the
same ID, it replaces the first, which then exits.

## Running as a service

### systemd

Units are in [`packaging/systemd`](packaging/systemd). Each file's
header has install steps.

```sh
systemctl enable --now khelam-server
systemctl reload khelam-server          # after editing /etc/khelam/tokens.yaml
systemctl enable --now khelam-agent@home  # uses /etc/khelam/agent-home.yaml
```

The agent unit passes the token as a systemd credential and stops
restarting on exit status 3.

### Containers

```sh
docker run -d --name khelam-server \
  -p 31000:31000/udp -p 41000-41999:41000-41999 -p 41000-41999:41000-41999/udp \
  -v khelam-server:/var/lib/khelam \
  -v ./tokens.yaml:/etc/khelam/tokens.yaml:ro \
  ghcr.io/prashunchitkr/khelam-server -tokens-file /etc/khelam/tokens.yaml

docker run -d --name khelam-agent --network host \
  -v khelam-agent:/var/lib/khelam -e KHELAM_TOKEN=khelam_… \
  ghcr.io/prashunchitkr/khelam-agent \
  -server my.server.example:31000 -fingerprint sha256:… -tunnel localhost:25565
```

Keep the volumes: they hold the server certificate (whose fingerprint
agents pin) and the agent ID (which keys sticky ports). The agent uses
`--network host` to reach services on the host's `localhost`.

## Development

Requires Go 1.27.

```sh
make build      # ./build/khelam-server, ./build/khelam-agent
make test       # go test -race ./...
make lint       # go vet + staticcheck
make images     # container images (docker or podman)
make snapshot   # release archives into dist/ via goreleaser
```

Releases are built by GitHub Actions on `v*` tags: binaries for Linux,
macOS and Windows, plus multi-arch images on GHCR.

### Layout

| Path | |
|---|---|
| `cmd/server`, `cmd/agent` | Entry points |
| `internal/server` | Agent sessions, port pools and reservations, TCP/UDP relaying |
| `internal/agent` | Registration, reconnects, relaying to local services, live status |
| `internal/tui` | The agent's terminal interface |
| `internal/transport` | QUIC sessions, streams and datagrams |
| `internal/protocol` | Control messages and datagram framing |
| `internal/auth` | Per-agent tokens |
| `internal/e2e` | End-to-end tests over loopback |
