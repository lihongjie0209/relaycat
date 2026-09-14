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
		Short: "manage Relaycat as a Windows service",
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
		Short: "register a Windows service",
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() < 0 {
				return errors.New("service command must follow --")
			}
			if len(args) == 0 {
				return errors.New("a Relaycat command is required after --")
			}
			if args[0] == "service" {
				return errors.New("a Windows service cannot run another service command")
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
			serviceArgs := []string{"service", "run", "--name", name, "--"}
			serviceArgs = append(serviceArgs, args...)
			if err := winservice.Install(winservice.InstallConfig{
				Name: name, DisplayName: displayName, Description: description,
				Executable: executable, Arguments: serviceArgs, Automatic: automatic,
			}); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "installed Windows service %s\n", name)
			return err
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&name, "name", defaultServiceName, "Windows service name")
	flags.StringVar(&displayName, "display-name", "", "Windows service display name")
	flags.StringVar(&description, "description", "Relaycat encrypted relay service", "Windows service description")
	flags.StringVar(&startup, "startup", "automatic", "automatic or manual")
	return cmd
}

func newServiceUninstallCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{Use: "uninstall", Short: "unregister a Windows service", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateServiceName(name); err != nil {
			return err
		}
		if err := winservice.Uninstall(name); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "uninstalled Windows service %s\n", name)
		return err
	}}
	cmd.Flags().StringVar(&name, "name", defaultServiceName, "Windows service name")
	return cmd
}

func newServiceStartCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{Use: "start", Short: "start a Windows service", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateServiceName(name); err != nil {
			return err
		}
		if err := winservice.Start(name); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "started Windows service %s\n", name)
		return err
	}}
	cmd.Flags().StringVar(&name, "name", defaultServiceName, "Windows service name")
	return cmd
}

func newServiceStopCommand() *cobra.Command {
	var name string
	var timeout time.Duration
	cmd := &cobra.Command{Use: "stop", Short: "stop a Windows service", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateServiceName(name); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		if err := winservice.Stop(ctx, name); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "stopped Windows service %s\n", name)
		return err
	}}
	cmd.Flags().StringVar(&name, "name", defaultServiceName, "Windows service name")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "maximum time to wait for shutdown")
	return cmd
}

func newServiceStatusCommand() *cobra.Command {
	var name, output string
	cmd := &cobra.Command{Use: "status", Short: "show Windows service status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
	cmd.Flags().StringVar(&name, "name", defaultServiceName, "Windows service name")
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
				return errors.New("a Windows service cannot run another service command")
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
	cmd.Flags().StringVar(&name, "name", defaultServiceName, "Windows service name")
	return cmd
}

func validateServiceName(name string) error {
	if name == "" || len(name) > 256 || strings.ContainsAny(name, "/\\\x00\r\n") {
		return errors.New("service name must be 1-256 characters without slashes or control characters")
	}
	return nil
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
