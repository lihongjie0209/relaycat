package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lihongjie0209/relaycat/internal/accesscode"
	"github.com/lihongjie0209/relaycat/internal/endpoint"
	"github.com/lihongjie0209/relaycat/internal/observability"
	"github.com/lihongjie0209/relaycat/internal/relay"
	"github.com/lihongjie0209/relaycat/internal/sshserver"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

type rootOptions struct{ config, logLevel, logFormat string }

func NewRoot(ctx context.Context, stdout, stderr io.Writer) *cobra.Command {
	opts := new(rootOptions)
	root := &cobra.Command{Use: "relaycat", Short: "end-to-end encrypted TCP tunnels over a gRPC relay", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.PersistentFlags().StringVar(&opts.config, "config", "", "optional YAML configuration file")
	root.PersistentFlags().StringVar(&opts.logLevel, "log-level", "info", "debug, info, warn, or error")
	root.PersistentFlags().StringVar(&opts.logFormat, "log-format", "text", "text or json")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error { return loadConfig(cmd, opts) }
	root.AddCommand(newRelayCommand(ctx, opts), newExposeCommand(ctx, opts), newServeCommand(ctx, opts), newConnectCommand(ctx, opts), newVersionCommand(), newCompletionCommand(root))
	return root
}

func Execute(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	shutdownTracing, err := observability.InitTracing(ctx, "relaycat")
	if err != nil {
		return fmt.Errorf("initializing tracing: %w", err)
	}
	cmd := NewRoot(ctx, stdout, stderr)
	cmd.SetArgs(args)
	runErr := cmd.ExecuteContext(ctx)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return errors.Join(runErr, shutdownTracing(shutdownCtx))
}

func loadConfig(cmd *cobra.Command, opts *rootOptions) error {
	v := viper.New()
	v.SetEnvPrefix("RELAYCAT")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()
	if opts.config != "" {
		v.SetConfigFile(opts.config)
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("reading config: %w", err)
		}
	} else if configDir, err := os.UserConfigDir(); err == nil {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(filepath.Join(configDir, "relaycat"))
		if err := v.ReadInConfig(); err != nil {
			var notFound viper.ConfigFileNotFoundError
			if !errors.As(err, &notFound) {
				return fmt.Errorf("reading config: %w", err)
			}
		}
	}
	var applyErr error
	apply := func(flag *pflag.Flag) {
		if applyErr != nil {
			return
		}
		if flag.Changed || !v.IsSet(flag.Name) {
			return
		}
		value := v.Get(flag.Name)
		var text string
		switch typed := value.(type) {
		case bool:
			text = strconv.FormatBool(typed)
		default:
			text = fmt.Sprint(value)
		}
		if err := flag.Value.Set(text); err != nil {
			applyErr = fmt.Errorf("setting %s from configuration: %w", flag.Name, err)
		}
	}
	cmd.Flags().VisitAll(apply)
	cmd.InheritedFlags().VisitAll(apply)
	return applyErr
}

func logger(w io.Writer, opts *rootOptions, relayMode bool) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(opts.logLevel)); err != nil {
		return nil, fmt.Errorf("invalid log level: %w", err)
	}
	format := opts.logFormat
	if relayMode && format == "text" {
		format = "json"
	}
	var h slog.Handler
	switch format {
	case "text":
		h = slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	case "json":
		h = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	default:
		return nil, errors.New("log format must be text or json")
	}
	return slog.New(h), nil
}

func newRelayCommand(ctx context.Context, root *rootOptions) *cobra.Command {
	var listen, cert, key, tokenFile, metrics string
	var h2c, publicH2C, noAuth, reflect, pprof bool
	var maxAgents, maxTunnels int
	var handshakeTimeout, shutdownTimeout time.Duration
	cmd := &cobra.Command{Use: "relay", Short: "run the public gRPC relay", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		log, err := logger(cmd.ErrOrStderr(), root, true)
		if err != nil {
			return err
		}
		token, err := endpoint.ReadToken(tokenFile, "RELAYCAT_AUTH_TOKEN")
		if err != nil {
			return err
		}
		return relay.RunServer(ctx, relay.ServerConfig{Listen: listen, TLSCert: cert, TLSKey: key, H2C: h2c, AllowPublicH2C: publicH2C, Token: token, NoAuth: noAuth, Reflection: reflect, MetricsListen: metrics, Pprof: pprof, ShutdownTimeout: shutdownTimeout, Logger: log, Service: relay.Config{MaxAgents: maxAgents, MaxTunnelsPerAgent: maxTunnels, HandshakeTimeout: handshakeTimeout}}, func(addr net.Addr) { _, _ = fmt.Fprintf(cmd.ErrOrStderr(), "relay listening on %s\n", addr) })
	}}
	f := cmd.Flags()
	f.StringVar(&listen, "listen", ":8443", "gRPC listen address")
	f.StringVar(&cert, "tls-cert", "", "TLS certificate file")
	f.StringVar(&key, "tls-key", "", "TLS private key file")
	f.BoolVar(&h2c, "h2c", false, "serve cleartext HTTP/2 for a trusted reverse proxy")
	f.BoolVar(&publicH2C, "allow-public-h2c", false, "allow h2c on a non-loopback address")
	f.StringVar(&tokenFile, "auth-token-file", "", "file containing the Relay bearer token")
	f.BoolVar(&noAuth, "no-auth", false, "disable Relay authentication even when a token is configured")
	f.BoolVar(&reflect, "reflection", false, "enable gRPC reflection")
	f.StringVar(&metrics, "metrics-listen", "", "optional Prometheus HTTP listen address")
	f.BoolVar(&pprof, "pprof", false, "serve pprof on the loopback metrics listener")
	f.IntVar(&maxAgents, "max-agents", 1000, "maximum registered agents")
	f.IntVar(&maxTunnels, "max-tunnels-per-agent", 128, "maximum concurrent tunnels per agent")
	f.DurationVar(&handshakeTimeout, "handshake-timeout", 10*time.Second, "maximum time to pair a tunnel")
	f.DurationVar(&shutdownTimeout, "shutdown-timeout", 15*time.Second, "graceful shutdown deadline")
	cmd.MarkFlagsMutuallyExclusive("h2c", "tls-cert")
	cmd.MarkFlagsMutuallyExclusive("h2c", "tls-key")
	cmd.MarkFlagsMutuallyExclusive("no-auth", "auth-token-file")
	return cmd
}

func newExposeCommand(ctx context.Context, root *rootOptions) *cobra.Command {
	var relayURL, target, state, tokenFile, caFile, output string
	var allowInsecure bool
	var idleTimeout time.Duration
	cmd := &cobra.Command{Use: "expose", Short: "expose one fixed TCP target through a Relay", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := accesscode.ValidateRelayURL(relayURL, allowInsecure); err != nil {
			return err
		}
		if _, _, err := net.SplitHostPort(target); err != nil {
			return fmt.Errorf("invalid target: %w", err)
		}
		if state != "" {
			state = expandHome(state)
			if _, err := os.Stat(state); err == nil {
				if err := accesscode.CheckPrivateFile(state); err != nil {
					return err
				}
			}
		}
		code, err := accesscode.LoadOrCreate(state, strings.TrimSuffix(relayURL, "/"), target)
		if err != nil {
			return err
		}
		encoded, err := accesscode.Encode(code)
		if err != nil {
			return err
		}
		switch output {
		case "plain":
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), encoded); err != nil {
				return fmt.Errorf("writing connection code: %w", err)
			}
		case "json":
			b, err := json.Marshal(map[string]string{"connection_code": encoded})
			if err != nil {
				return fmt.Errorf("encoding output: %w", err)
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(b)); err != nil {
				return fmt.Errorf("writing connection code: %w", err)
			}
		default:
			return errors.New("output must be plain or json")
		}
		token, err := endpoint.ReadToken(tokenFile, "RELAYCAT_TOKEN")
		if err != nil {
			return err
		}
		log, err := logger(cmd.ErrOrStderr(), root, false)
		if err != nil {
			return err
		}
		return endpoint.RunAgent(ctx, endpoint.AgentConfig{Code: code, Target: target, Token: token, CAFile: caFile, AllowInsecure: allowInsecure, IdleTimeout: idleTimeout, Logger: log})
	}}
	f := cmd.Flags()
	f.StringVar(&relayURL, "relay", "", "Relay URL, for example https://relay.example.com:8443")
	f.StringVar(&target, "target", "", "fixed TCP target in host:port form")
	f.StringVar(&state, "state", "", "optional persistent secret state file")
	f.StringVar(&tokenFile, "token-file", "", "Relay bearer token file")
	f.StringVar(&caFile, "ca-file", "", "additional CA certificate file")
	f.BoolVar(&allowInsecure, "allow-insecure-relay", false, "allow a cleartext http Relay URL")
	f.StringVar(&output, "output", "plain", "plain or json")
	f.DurationVar(&idleTimeout, "idle-timeout", 30*time.Minute, "close a tunnel after this much TCP inactivity")
	_ = cmd.MarkFlagRequired("relay")
	_ = cmd.MarkFlagRequired("target")
	return cmd
}

func newConnectCommand(ctx context.Context, root *rootOptions) *cobra.Command {
	var listen, tokenFile, caFile string
	var allowInsecure, once, allowPublic bool
	var idleTimeout time.Duration
	cmd := &cobra.Command{Use: "connect <connection-code>", Short: "listen locally and forward TCP through the Relay", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		code, err := accesscode.Decode(args[0], allowInsecure)
		if err != nil {
			return err
		}
		if !allowPublic && !isLoopbackAddress(listen) {
			return errors.New("local listener must use a loopback address unless --allow-public-listen is set")
		}
		token, err := endpoint.ReadToken(tokenFile, "RELAYCAT_TOKEN")
		if err != nil {
			return err
		}
		log, err := logger(cmd.ErrOrStderr(), root, false)
		if err != nil {
			return err
		}
		return endpoint.RunClient(ctx, endpoint.ClientConfig{Code: code, Listen: listen, Token: token, CAFile: caFile, AllowInsecure: allowInsecure, Once: once, IdleTimeout: idleTimeout, Logger: log}, func(addr net.Addr) { _, _ = fmt.Fprintln(cmd.OutOrStdout(), addr.String()) })
	}}
	f := cmd.Flags()
	f.StringVar(&listen, "listen", "127.0.0.1:0", "local TCP listen address")
	f.StringVar(&tokenFile, "token-file", "", "Relay bearer token file")
	f.StringVar(&caFile, "ca-file", "", "additional CA certificate file")
	f.BoolVar(&allowInsecure, "allow-insecure-relay", false, "allow a cleartext http Relay URL")
	f.BoolVar(&allowPublic, "allow-public-listen", false, "allow the local listener on a non-loopback address")
	f.BoolVar(&once, "once", false, "accept one local connection and exit")
	f.DurationVar(&idleTimeout, "idle-timeout", 30*time.Minute, "close a tunnel after this much TCP inactivity")
	return cmd
}

func newServeCommand(ctx context.Context, root *rootOptions) *cobra.Command {
	var relayURL, state, tokenFile, caFile, hostKey, output string
	var authorizedKeyFiles []string
	var allowInsecure bool
	var idleTimeout time.Duration
	cmd := &cobra.Command{
		Use:       "serve <ssh|no-auth-ssh>",
		Short:     "serve a built-in service through a Relay",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"ssh", "no-auth-ssh"},
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := args[0]
			if mode != "ssh" && mode != "no-auth-ssh" {
				return fmt.Errorf("unknown service %q", mode)
			}
			if err := accesscode.ValidateRelayURL(relayURL, allowInsecure); err != nil {
				return err
			}
			var authorizedKeys []string
			for _, path := range authorizedKeyFiles {
				data, err := os.ReadFile(expandHome(path)) // #nosec G304 -- explicitly selected authorized_keys file.
				if err != nil {
					return fmt.Errorf("reading authorized keys file: %w", err)
				}
				authorizedKeys = append(authorizedKeys, string(data))
			}
			if mode == "ssh" && len(authorizedKeys) == 0 {
				return errors.New("ssh requires at least one --authorized-keys-file")
			}
			if mode == "no-auth-ssh" && len(authorizedKeys) != 0 {
				return errors.New("--authorized-keys-file cannot be used with no-auth-ssh")
			}
			log, err := logger(cmd.ErrOrStderr(), root, false)
			if err != nil {
				return err
			}
			server, err := sshserver.New(sshserver.Config{
				HostKeyPath:    expandHome(hostKey),
				AuthorizedKeys: authorizedKeys,
				NoClientAuth:   mode == "no-auth-ssh",
				Logger:         log,
			})
			if err != nil {
				return fmt.Errorf("initializing SSH server: %w", err)
			}
			if state != "" {
				state = expandHome(state)
				if _, err := os.Stat(state); err == nil {
					if err := accesscode.CheckPrivateFile(state); err != nil {
						return err
					}
				}
			}
			code, err := accesscode.LoadOrCreate(state, strings.TrimSuffix(relayURL, "/"), "builtin:"+mode)
			if err != nil {
				return err
			}
			encoded, err := accesscode.Encode(code)
			if err != nil {
				return err
			}
			if err := writeConnectionCode(cmd.OutOrStdout(), output, encoded); err != nil {
				return err
			}
			if mode == "no-auth-ssh" {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: anyone with this connection code can run commands as the current user")
			}
			token, err := endpoint.ReadToken(tokenFile, "RELAYCAT_TOKEN")
			if err != nil {
				return err
			}
			return endpoint.RunAgent(ctx, endpoint.AgentConfig{
				Code: code, Token: token, CAFile: caFile, AllowInsecure: allowInsecure,
				IdleTimeout: idleTimeout, Logger: log, Handler: server.HandleConn,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&relayURL, "relay", "", "Relay URL, for example https://relay.example.com")
	f.StringVar(&state, "state", "", "optional persistent connection-code state file")
	f.StringVar(&tokenFile, "token-file", "", "Relay bearer token file")
	f.StringVar(&caFile, "ca-file", "", "additional CA certificate file")
	f.StringVar(&hostKey, "host-key", "", "persistent Ed25519 SSH host key path")
	f.StringSliceVar(&authorizedKeyFiles, "authorized-keys-file", nil, "OpenSSH authorized_keys file; may be repeated")
	f.BoolVar(&allowInsecure, "allow-insecure-relay", false, "allow a cleartext http Relay URL")
	f.StringVar(&output, "output", "plain", "plain or json")
	f.DurationVar(&idleTimeout, "idle-timeout", 30*time.Minute, "close a tunnel after this much inactivity")
	_ = cmd.MarkFlagRequired("relay")
	return cmd
}

func writeConnectionCode(w io.Writer, output, encoded string) error {
	switch output {
	case "plain":
		if _, err := fmt.Fprintln(w, encoded); err != nil {
			return fmt.Errorf("writing connection code: %w", err)
		}
	case "json":
		data, err := json.Marshal(map[string]string{"connection_code": encoded})
		if err != nil {
			return fmt.Errorf("encoding output: %w", err)
		}
		if _, err := fmt.Fprintln(w, string(data)); err != nil {
			return fmt.Errorf("writing connection code: %w", err)
		}
	default:
		return errors.New("output must be plain or json")
	}
	return nil
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{Use: "version", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "relaycat %s (%s, %s)\n", Version, Commit, Date)
		return err
	}}
}
func newCompletionCommand(root *cobra.Command) *cobra.Command {
	return &cobra.Command{Use: "completion [bash|zsh|fish|powershell]", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return root.GenBashCompletion(cmd.OutOrStdout())
		case "zsh":
			return root.GenZshCompletion(cmd.OutOrStdout())
		case "fish":
			return root.GenFishCompletion(cmd.OutOrStdout(), true)
		case "powershell":
			return root.GenPowerShellCompletion(cmd.OutOrStdout())
		default:
			return errors.New("unsupported shell")
		}
	}}
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
func isLoopbackAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

var _ = time.Second
