package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lihongjie0209/relaycat/internal/winservice"
	"github.com/spf13/cobra"
)

const defaultServiceName = "relaycat"

func newServiceCommand(stdout, stderr io.Writer) *cobra.Command {
	serviceCmd := &cobra.Command{
		Use:   "service",
		Short: "manage Relaycat as a system service",
	}
	serviceCmd.AddCommand(
		newServiceInstallCommand(),
		newServiceUninstallCommand(),
		newServiceStartCommand(),
		newServiceStopCommand(),
		newServiceStatusCommand(),
		newServiceRunCommand(stdout, stderr),
	)
	return serviceCmd
}

func newServiceInstallCommand() *cobra.Command {
	var name, displayName, description, startup string
	cmd := &cobra.Command{
		Use:   "install [flags] -- <relaycat command and arguments>",
		Short: "register a Windows or systemd service",
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() < 0 {
				return errors.New("service command must follow --")
			}
			if len(args) == 0 {
				return errors.New("a Relaycat command is required after --")
			}
			if args[0] == "service" {
				return errors.New("a service cannot run another service command")
			}
			return validateServiceName(name)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			automatic, err := parseStartup(startup)
			if err != nil {
				return err
			}
			executable, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locating Relaycat executable: %w", err)
			}
			if displayName == "" {
				displayName = name
			}
			if err := winservice.Install(winservice.InstallConfig{
				Name: name, DisplayName: displayName, Description: description,
				Executable: executable, Arguments: args, Automatic: automatic,
			}); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "installed service %s\n", name)
			return err
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&name, "name", defaultServiceName, "service name")
	flags.StringVar(&displayName, "display-name", "", "Windows service display name")
	flags.StringVar(&description, "description", "Relaycat encrypted relay service", "service description")
	flags.StringVar(&startup, "startup", "automatic", "automatic or manual")
	return cmd
}

func newServiceUninstallCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{Use: "uninstall", Short: "unregister a Windows or systemd service", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateServiceName(name); err != nil {
			return err
		}
		if err := winservice.Uninstall(name); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "uninstalled service %s\n", name)
		return err
	}}
	cmd.Flags().StringVar(&name, "name", defaultServiceName, "service name")
	return cmd
}

func newServiceStartCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{Use: "start", Short: "start a Windows or systemd service", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateServiceName(name); err != nil {
			return err
		}
		if err := winservice.Start(name); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "started service %s\n", name)
		return err
	}}
	cmd.Flags().StringVar(&name, "name", defaultServiceName, "service name")
	return cmd
}

func newServiceStopCommand() *cobra.Command {
	var name string
	var timeout time.Duration
	cmd := &cobra.Command{Use: "stop", Short: "stop a Windows or systemd service", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateServiceName(name); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		if err := winservice.Stop(ctx, name); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "stopped service %s\n", name)
		return err
	}}
	cmd.Flags().StringVar(&name, "name", defaultServiceName, "service name")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "maximum time to wait for shutdown")
	return cmd
}

func newServiceStatusCommand() *cobra.Command {
	var name, output string
	cmd := &cobra.Command{Use: "status", Short: "show Windows or systemd service status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateServiceName(name); err != nil {
			return err
		}
		status, err := winservice.Query(name)
		if err != nil {
			return err
		}
		switch output {
		case "plain":
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", status.State)
		case "json":
			var data []byte
			data, err = json.Marshal(status)
			if err == nil {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			}
		default:
			return errors.New("output must be plain or json")
		}
		return err
	}}
	cmd.Flags().StringVar(&name, "name", defaultServiceName, "service name")
	cmd.Flags().StringVar(&output, "output", "plain", "plain or json")
	return cmd
}

func newServiceRunCommand(stdout, stderr io.Writer) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:    "run --name <name> -- <relaycat command and arguments>",
		Short:  "run under the Windows Service Control Manager",
		Hidden: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() < 0 || len(args) == 0 {
				return errors.New("a Relaycat command is required after --")
			}
			if args[0] == "service" {
				return errors.New("a service cannot run another service command")
			}
			return validateServiceName(name)
		},
		RunE: func(_ *cobra.Command, args []string) error {
			return winservice.Run(name, func(ctx context.Context) error {
				command := NewRoot(ctx, stdout, stderr)
				command.SetArgs(args)
				return command.ExecuteContext(ctx)
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", defaultServiceName, "service name")
	return cmd
}

func validateServiceName(name string) error {
	if name == "" || len(name) > 256 || !isServiceNameStart(name[0]) {
		return errors.New("service name must be 1-256 characters using letters, digits, dot, underscore, at, or hyphen")
	}
	for index := 1; index < len(name); index++ {
		if !isServiceNameStart(name[index]) && !strings.ContainsRune("._@-", rune(name[index])) {
			return errors.New("service name must be 1-256 characters using letters, digits, dot, underscore, at, or hyphen")
		}
	}
	return nil
}

func isServiceNameStart(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func parseStartup(value string) (bool, error) {
	switch value {
	case "automatic":
		return true, nil
	case "manual":
		return false, nil
	default:
		return false, errors.New("startup must be automatic or manual")
	}
}
