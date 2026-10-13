package main

import (
	"fmt"
	"slices"

	"github.com/openclaw/wacli/agent"
	"github.com/spf13/cobra"
)

// The agent namespace owns workflow safety; upstream commands continue to own
// WhatsApp protocol mechanics. Child flags are parsed by the packaged helper.
func newAgentCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:                "agent <send-text|send-file|recent-reply|monitor-check|health|live|transcribe> [flags]",
		Short:              "Standalone verified messaging and read-only agent workflows (Node 24+)",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
				return cmd.Help()
			}
			// Cobra passes leading root flags through an unparsed child. Consume
			// only that prefix, stopping at the workflow name; its remaining
			// payload/helper flags must reach Node byte-for-byte.
			globals := cmd.Root().PersistentFlags()
			globals.SetInterspersed(false)
			if err := globals.Parse(args); err != nil {
				return err
			}
			args = globals.Args()
			if len(args) == 0 {
				return fmt.Errorf("an agent workflow is required; see docs/agent.md")
			}
			store, err := resolveStoreDir(flags)
			if err != nil {
				return err
			}
			if flags.asJSON && !slices.Contains(args, "--json") {
				args = append(args, "--json")
			}
			if slices.Contains([]string{"send-text", "send-file", "configure-transcription"}, args[0]) {
				if err := flags.requireWritable(); err != nil {
					return err
				}
			}
			return agent.Run(cmd.Context(), store, flags.isReadOnly(), args)
		},
	}
}
