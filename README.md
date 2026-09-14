# Relaycat

Relaycat creates end-to-end encrypted TCP tunnels through a gRPC relay. Both
ends make outbound connections to the relay; Relaycat never performs NAT hole
punching and never switches to a direct peer-to-peer path.

The design is inspired by Tailcat's "address as capability" model, but Relaycat
has an independent protocol and does not depend on Tailscale, DERP, WireGuard,
STUN, a TUN device, or a control plane.

## How it works

```text
local TCP client -> relaycat connect == gRPC/TLS ==> Relay <== gRPC/TLS == relaycat expose -> fixed TCP target
                                       \________ Noise NNpsk0 E2E ciphertext ________/
```

The Relay sees routing IDs, connection timing, and ciphertext sizes. It cannot
read the target address or TCP payload. A connection code (`rc1_...`) contains
the Relay URL, a random routing ID, and a 256-bit Noise preshared key. Treat it
as a password.

## Build

Go 1.25 or newer is required.

```sh
make generate
make test
make build
```

## Quick start

For a local development Relay without TLS or authentication:

```sh
relaycat relay --listen 127.0.0.1:8080 --h2c
```

Expose a local SSH server:

```sh
relaycat expose \
  --relay http://127.0.0.1:8080 \
  --allow-insecure-relay \
  --target 127.0.0.1:22
```

The command prints an `rc1_...` code. On the client:

```sh
relaycat connect rc1_xxx --allow-insecure-relay --listen 127.0.0.1:2222
ssh -p 2222 user@127.0.0.1
```

`connect` accepts concurrent local connections until interrupted. Add `--once`
to accept one connection.

## Production Relay

Generate a high-entropy Relay bandwidth token and store it with restrictive
permissions:

```sh
openssl rand -base64 32 > relay.token
chmod 600 relay.token

relaycat relay --listen :8443 \
  --tls-cert /etc/relaycat/fullchain.pem \
  --tls-key /etc/relaycat/privkey.pem \
  --auth-token-file relay.token \
  --metrics-listen 127.0.0.1:9090
```

The Relay token is optional. When the Relay has no token configured, endpoints
connect without one. When `--auth-token-file` or `RELAYCAT_AUTH_TOKEN` configures
a Relay token, both endpoint processes must provide the matching value. It is
deliberately not embedded in the connection code:

```sh
relaycat expose --relay https://relay.example.com:8443 \
  --target 127.0.0.1:22 --token-file relay.token

relaycat connect rc1_xxx --token-file relay.token --listen 127.0.0.1:2222
```

For a private CA, pass `--ca-file`. Relaycat does not provide an option to skip
TLS certificate verification.

### Reverse proxy

Run Relaycat as h2c on loopback and configure the proxy's upstream to use
cleartext HTTP/2:

```sh
relaycat relay --listen 127.0.0.1:8080 --h2c --auth-token-file relay.token
```

The externally advertised URL remains `https://relay.example.com`. With Caddy:

```caddyfile
relay.example.com {
    reverse_proxy h2c://127.0.0.1:8080
}
```

## Persistent connection codes

By default, `expose` generates a fresh code for each run. Persist one explicitly:

```sh
relaycat expose ... --state ~/.config/relaycat/ssh.json
```

The file is created with mode `0600` on Unix. Relaycat refuses to reuse it with
a different Relay URL or target. Delete the file to rotate the code. Existing
TCP sessions are not resumable after a process or network failure.

## Configuration

Flags take precedence over environment variables and YAML. Environment
variables use the `RELAYCAT_` prefix and replace dashes with underscores, for
example `RELAYCAT_LOG_LEVEL` and `RELAYCAT_IDLE_TIMEOUT`. Relaycat reads
`config.yaml` from the platform user configuration directory's `relaycat`
subdirectory, or a file selected with `--config`.

## Operations

- Standard gRPC health checking is registered. `/healthz` and `/metrics` are
  available when `--metrics-listen` is set.
- Important limits: `--max-agents` and `--max-tunnels-per-agent`.
- Relay state is in memory and a v1 deployment is a single instance. Restarting
  it disconnects agents and tunnels; agents reconnect with exponential backoff.
- Structured Relay logs are JSON. Codes, PSKs, tokens, target addresses, and
  plaintext are never logged.
- Prometheus alerts and an importable Grafana dashboard are in
  `deploy/monitoring/`. Add `--pprof` to expose profiling endpoints on a
  loopback-only metrics listener.
- Set `OTEL_EXPORTER_OTLP_ENDPOINT` (or
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`) to export gRPC client and server traces
  using the standard OpenTelemetry OTLP environment configuration.

## Security boundaries

- A connection code grants access to exactly one Agent-selected TCP target.
- The optional Relay token controls bandwidth usage; the Noise PSK provides
  end-to-end authentication and encryption independently of the Relay.
- Relaycat uses Noise `NNpsk0` with X25519, ChaCha20-Poly1305, and BLAKE2s. The
  route and session IDs are bound into every handshake.
- Traffic sizes, timing, source IP addresses, and routing IDs remain visible to
  the Relay. Relaycat does not provide traffic padding or anonymity.

## Non-goals for v1

UDP, SOCKS, a built-in shell, user accounts, multi-tenant quotas, Relay
clustering, direct paths, and session resumption are intentionally out of scope.

## License

BSD-3-Clause.
