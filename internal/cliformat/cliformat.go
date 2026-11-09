// Package cliformat formats output for the gritz CLI.
package cliformat

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/gritzapp/gritz/internal/configfile"
)

// WriteConfigEntries writes merged's entries as a table, marking the default
// (merged.Org) with *. It never writes credentials. Call merged.Validate first.
func WriteConfigEntries(w io.Writer, merged *configfile.Merged) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\tNAME\tID\tSERVER\tLOGIN")
	for _, e := range merged.Entries() {
		mark, login := "", "oauth"
		if e.Name == merged.Org {
			mark = "*"
		}
		if e.Key != "" {
			login = "key"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", mark, e.Name, e.ID, e.Server, login)
	}
	return tw.Flush()
}
