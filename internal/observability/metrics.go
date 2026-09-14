package observability

import "github.com/prometheus/client_golang/prometheus"

var (
	Agents      = prometheus.NewGauge(prometheus.GaugeOpts{Name: "relaycat_agents", Help: "Currently registered agents."})
	Tunnels     = prometheus.NewGauge(prometheus.GaugeOpts{Name: "relaycat_tunnels", Help: "Currently active or pending tunnels."})
	TunnelOpens = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "relaycat_tunnel_opens_total", Help: "Tunnel open attempts by result."}, []string{"result"})
	Bytes       = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "relaycat_relayed_bytes_total", Help: "Ciphertext bytes relayed by direction."}, []string{"direction"})
	AuthRejects = prometheus.NewCounter(prometheus.CounterOpts{Name: "relaycat_auth_rejects_total", Help: "Rejected RPC authentication attempts."})
)

func init() {
	prometheus.MustRegister(Agents, Tunnels, TunnelOpens, Bytes, AuthRejects)
}
