---
name: relaycat
description: Operate, deploy, troubleshoot, and test Relaycat encrypted TCP tunnels and built-in SSH services. Use this skill whenever a user mentions Relaycat, rc1 connection codes, relaycat relay/expose/connect/serve, Relaycat systemd or Windows services, relay.ximeibao.cn, or asks to transfer files or SSH through a Relaycat relay.
compatibility: Requires the relaycat CLI for runtime operations. Deployment may additionally require systemctl, Windows SCM, GitHub CLI, or a secret broker such as passman.
---

# Relaycat operations

Relaycat carries end-to-end encrypted TCP streams over a gRPC relay. Both
endpoints make outbound connections; it does not use NAT hole punching or
switch to a direct path.

Work from the repository root when changing code. Read `README.md` for the
current public interface and use `relaycat <command> --help` to verify flags
against the installed version before operating a remote machine.

## Safety model

- Treat every `rc1_...` connection code as a password. It contains routing
  information and the Noise PSK.
- Never print, log, transform, commit, or place a real connection code in a
  shell argument that will be recorded remotely.
- Use a secret broker when a catalog entry is available. With passman, inject
  the code without revealing it:

  ```sh
  passman run \
    --env RELAYCAT_CODE=path/to/entry#connection_code \
    -- sh -c 'exec relaycat connect "$RELAYCAT_CODE" --listen 127.0.0.1:2222'
  ```

- Bind local forwarding listeners to `127.0.0.1` unless the user explicitly
  asks for LAN/public exposure and understands the access boundary.
- Prefer TLS Relay URLs. Allow h2c only on loopback or an explicitly trusted
  private network.
- A Relay bandwidth token is optional. Supply a token only when the Relay is
  configured to require one. It is separate from the end-to-end connection
  code.
- `serve no-auth-ssh` gives anyone holding the code command execution as the
  service account. Prefer `serve ssh` with authorized keys for general use.
- Preserve existing services, state files, host keys, authorized keys, and
  unrelated configuration. Use distinct service names when adding a second
  endpoint.

## Select the command

Use this mapping:

| Goal | Command |
| --- | --- |
| Run the central broker | `relaycat relay` |
| Publish an existing TCP service | `relaycat expose` |
| Open a local listener for a code | `relaycat connect` |
| Publish the built-in SSH server | `relaycat serve ssh` |
| Publish deliberately unauthenticated SSH | `relaycat serve no-auth-ssh` |
| Register a long-running OS service | `relaycat service install` |

## Common workflows

### Connect and SSH

Keep the connector running in one terminal:

```sh
relaycat connect rc1_REDACTED --listen 127.0.0.1:2222
```

Then connect in another terminal:

```sh
ssh -p 2222 user@127.0.0.1
```

For a `no-auth-ssh` endpoint, clients may need:

```sh
ssh -p 2222 \
  -o PubkeyAuthentication=no \
  -o PasswordAuthentication=no \
  relaycat@127.0.0.1
```

The username is ignored by Relaycat's no-auth SSH mode.

### Expose a TCP target

Persist the generated code so restarts do not rotate it:

```sh
relaycat expose \
  --relay https://relay.example.com \
  --target 127.0.0.1:22 \
  --state /var/lib/relaycat/ssh-state.json
```

Deleting the state file rotates the code. Do not delete it during upgrades.

### Built-in SSH

Authenticated mode:

```sh
relaycat serve ssh \
  --relay https://relay.example.com \
  --authorized-keys-file /etc/relaycat/authorized_keys \
  --state /var/lib/relaycat/ssh-state.json \
  --host-key /var/lib/relaycat/ssh-host-ed25519 \
  --connection-code-file /var/lib/relaycat/ssh.code
```

Use `--connection-code-file` for services without a valid stdout handle,
especially Windows SCM. Protect the state, host-key, token, and code files so
only the service account and administrators can read them.

### Linux systemd

Run as root and place the managed Relaycat command after `--`:

```sh
relaycat service install --name relaycat-ssh -- \
  serve ssh \
  --relay https://relay.example.com \
  --authorized-keys-file /etc/relaycat/authorized_keys \
  --state /var/lib/relaycat/ssh-state.json \
  --host-key /var/lib/relaycat/ssh-host-ed25519 \
  --connection-code-file /var/lib/relaycat/ssh.code

relaycat service start --name relaycat-ssh
relaycat service status --name relaycat-ssh
```

Check both process state and application health:

```sh
systemctl is-active relaycat-ssh.service
journalctl -u relaycat-ssh.service --since '-10 minutes' --no-pager
```

### Windows Service

Use an elevated PowerShell terminal and absolute paths:

```powershell
relaycat service install --name relaycat-ssh -- `
  serve ssh `
  --relay https://relay.example.com `
  --authorized-keys-file C:\ProgramData\relaycat\authorized_keys `
  --state C:\ProgramData\relaycat\ssh-state.json `
  --host-key C:\ProgramData\relaycat\ssh-host-ed25519 `
  --connection-code-file C:\ProgramData\relaycat\ssh.code

relaycat service start --name relaycat-ssh
relaycat service status --name relaycat-ssh
```

Relaycat services use `LocalSystem` by default. Verify the impact before using
no-auth SSH in this mode.

## File transfer through built-in SSH

The built-in SSH server currently has no SFTP subsystem, so do not claim that
`scp` or `sftp` works. Transfer binary data through an SSH exec channel using
PowerShell standard input/output, or expose an existing system SSH server when
native SFTP is required.

After every transfer, compare SHA-256 hashes at both ends. Avoid printing file
contents or hashes of secrets.

## Deployment workflow

1. Resolve the exact host, OS, architecture, installed version, binary path,
   service name, and current health with read-only commands.
2. Select the matching signed/released archive and verify it against
   `checksums.txt` before extraction.
3. Preserve the previous binary as a rollback copy.
4. Install with an atomic rename when the OS permits it.
5. Restart only the Relaycat service in scope.
6. If restart or health checks fail, restore the old binary and restart it.
7. Verify `relaycat version`, service state, listening sockets, and a real
   end-to-end connection.
8. Remove temporary credentials, test data, connection-code copies, and
   temporary binaries. Retain deliberate rollback artifacts and say where
   they are.

Do not publish a tag or GitHub Release unless the user asks for a release.

## Verification

For code changes, run checks proportional to the change:

```sh
go test -race -shuffle=on ./...
go test -race -count=1 -tags=integration ./...
golangci-lint run
go vet ./...
```

Systemd integration tests require Docker/Testcontainers. Cross-compile service
changes for Windows even when working on Linux.

For a deployed tunnel, verify separately:

- Relaycat handshake: local TCP connect until the target protocol banner.
- Application handshake: for example, a complete fresh SSH command.
- Steady-state transfer: upload and download generated non-secret data and
  compare hashes.
- Stability: keep the connector process alive, restart the endpoint service,
  and confirm the same connector resumes accepting successful sessions.

Do not infer bandwidth from ping. Report RTT, Relaycat handshake, application
handshake, upload throughput, and download throughput independently.

## Troubleshooting

- First connection slow but later connections fast: verify that the client
  version prewarms its gRPC/TLS transport before reporting ready.
- Service immediately stops on Windows: use `--connection-code-file`; service
  stdout may not be writable.
- Code changes after restart: add `--state` and preserve that file.
- SSH fingerprint changes: add `--host-key` and preserve that file.
- Agent disappears after a network interruption: inspect reconnect/backoff
  logs and verify the Relay URL, TLS trust, SNI route, and optional token.
- Good ping but slow one-way file transfer: measure directions independently;
  asymmetric uplink limits are common.
- `SendMsg called after CloseSend`: check half-close handling and confirm both
  endpoints use a release containing the handler bridge fix.
- SNI routing failure: confirm the advertised Relay URL hostname matches the
  TLS certificate and the proxy routes HTTP/2/TLS to the Relay backend.

## Handoff

Report the installed version, host/service names, health checks, connection
method, test results, rollback location, and cleanup performed. Never include
real connection codes, PSKs, bearer tokens, private keys, or unredacted command
lines containing them.
