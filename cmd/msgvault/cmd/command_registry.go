package cmd

import "github.com/spf13/cobra"

// Factories are registered only during package initialization. Every production
// execution owns a new command tree and all of its mutable flag bindings.
var commandFactories []func() *cobra.Command

func registerCommandFactory(factory func() *cobra.Command) {
	commandFactories = append(commandFactories, factory)
}

func newProductionRootCommand() *cobra.Command {
	root := newRootCommand()
	for _, factory := range commandFactories {
		root.AddCommand(factory())
	}
	return root
}
