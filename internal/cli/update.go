package cli

import (
	"fmt"

	"github.com/lihongjie0209/relaycat/internal/selfupdate"
	"github.com/spf13/cobra"
)

func newUpdateCommand() *cobra.Command {
	var version string
	var check, force, compact, standard bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "download and install a verified Relaycat release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := selfupdate.Run(cmd.Context(), Version, selfupdate.Options{
				Version: version, Check: check, Force: force, Compact: compact, Standard: standard,
			})
			if err != nil {
				return err
			}
			if check {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "current: %s\nlatest: %s\nasset: %s\n", result.Current, result.Latest, result.Asset)
				return err
			}
			if !result.Updated {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "relaycat %s is already installed\n", result.Current)
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "updated relaycat %s to %s at %s\nrestart any running Relaycat service to use the new binary\n", result.Current, result.Latest, result.Path)
			if err != nil {
				return err
			}
			if result.Backup != "" {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "previous binary retained at %s until it is no longer in use\n", result.Backup)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&version, "version", "latest", "release tag, or latest")
	cmd.Flags().BoolVar(&check, "check", false, "check the available version without installing")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if the version matches")
	cmd.Flags().BoolVar(&compact, "compact", false, "use the compact UPX package where available")
	cmd.Flags().BoolVar(&standard, "standard", false, "use the standard package")
	cmd.MarkFlagsMutuallyExclusive("compact", "standard")
	return cmd
}
