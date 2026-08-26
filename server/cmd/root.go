// Package cmd wires the command-line interface.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/t0mer/renfild/internal/version"
)

var configFile string

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "renfild",
		Short: "Renfild — a self-hosted, speaker-aware voice assistant",
		Long: "Renfild receives audio from wake-word satellites, works out who is\n" +
			"speaking, transcribes the command, routes it to an intent handler and\n" +
			"speaks the answer back. Everything runs on your own hardware.",
		Version:      version.Version,
		SilenceUsage: true,
		// Running the binary with no arguments starts the server, which is what
		// the systemd unit does.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd)
		},
	}
	root.PersistentFlags().StringVar(&configFile, "config", "", "path to config.yaml")
	addServeFlags(root.Flags())
	root.AddCommand(newServeCmd())
	return root
}

// Execute runs the CLI.
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
