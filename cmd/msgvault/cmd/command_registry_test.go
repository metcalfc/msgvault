package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func commandCatalog(root *cobra.Command) map[string]*cobra.Command {
	catalog := make(map[string]*cobra.Command)
	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		catalog[command.CommandPath()] = command
		for _, child := range command.Commands() {
			visit(child)
		}
	}
	visit(root)
	return catalog
}

func TestProductionRootOwnsIndependentCommandTrees(t *testing.T) {
	first := commandCatalog(newProductionRootCommand())
	second := commandCatalog(newProductionRootCommand())
	require.Len(t, second, len(first))
	for path, command := range first {
		t.Run(path, func(t *testing.T) {
			peer := second[path]
			require.NotNil(t, peer)
			assert.NotSame(t, command, peer)
			assert.Equal(t, command.Use, peer.Use)
			assert.Equal(t, command.Aliases, peer.Aliases)
			for _, flags := range [][2]*pflag.FlagSet{{command.Flags(), peer.Flags()}, {command.PersistentFlags(), peer.PersistentFlags()}} {
				flags[0].VisitAll(func(flag *pflag.Flag) {
					other := flags[1].Lookup(flag.Name)
					require.NotNil(t, other)
					assert.NotSame(t, flag, other)
					assert.NotSame(t, flag.Value, other.Value, "--%s has shared mutable storage", flag.Name)
					assert.Equal(t, flag.DefValue, other.DefValue)
					// Slice/array flag values wrap a pointer; distinct wrappers alone
					// do not prove the underlying option storage is independent.
					if value, ok := independentFlagValue(flag.Value.Type()); ok {
						before := other.Value.String()
						require.NoError(t, flag.Value.Set(value))
						assert.Equal(t, before, other.Value.String(), "--%s leaked into another root", flag.Name)
					}
				})
			}
		})
	}
}

func independentFlagValue(kind string) (string, bool) {
	switch kind {
	case "string", "stringSlice", "stringArray":
		return "factory-isolation-value", true
	case "bool":
		return "true", true
	case "int", "int32", "int64", "uint", "uint32", "uint64", "float32", "float64", "count":
		return "37", true
	case "duration":
		return "37s", true
	default:
		return "", false
	}
}

func TestProductionRootFlagsDoNotLeakAcrossInvocations(t *testing.T) {
	first := newProductionRootCommand()
	query, _, err := first.Find([]string{"query"})
	require.NoError(t, err)
	require.NoError(t, query.Flags().Set("format", "json"))
	require.NoError(t, query.Flags().Set("stream", "true"))
	add, _, err := first.Find([]string{"add-calendar"})
	require.NoError(t, err)
	require.NoError(t, add.Flags().Set("calendars", "primary,team"))
	second := newProductionRootCommand()
	assert.Equal(t, "json", query.Flags().Lookup("format").Value.String())
	firstCalendars, err := add.Flags().GetStringSlice("calendars")
	require.NoError(t, err)
	assert.Equal(t, []string{"primary", "team"}, firstCalendars)
	nextQuery, _, err := second.Find([]string{"query"})
	require.NoError(t, err)
	assert.Equal(t, nextQuery.Flags().Lookup("format").DefValue, nextQuery.Flags().Lookup("format").Value.String())
	assert.False(t, nextQuery.Flags().Changed("stream"))
	nextAdd, _, err := second.Find([]string{"add-calendar"})
	require.NoError(t, err)
	calendars, err := nextAdd.Flags().GetStringSlice("calendars")
	require.NoError(t, err)
	assert.Empty(t, calendars)
}
