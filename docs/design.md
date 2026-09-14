# Protocol design

The `relay.v1.RelayService` API has three streaming methods:

1. `RegisterAgent` holds an Agent registration and delivers incoming sessions.
2. `Connect` carries one Client-side TCP connection.
3. `Accept` carries the matching Agent-side TCP connection.

The Client first registers a route and receives a random session ID. Only then
does it construct a Noise prologue from `relaycat/v1 || route ID || session ID`.
The Relay forwards the ClientHello to the Agent, and the Agent returns its
response over `Accept`. All subsequent frames are encrypted `PlainFrame`
messages. A separate gRPC stream is used for every TCP connection.

The Relay uses bounded, unbuffered channels to preserve backpressure. It never
parses encrypted frames. `DATA`, `CLOSE_WRITE`, and `CLOSE` preserve TCP
half-close behavior. Cancellation of either gRPC stream tears down the session.

Tailcat inspired the capability-address UX. No Tailcat source code is copied in
the initial implementation and no Tailscale package is linked.
