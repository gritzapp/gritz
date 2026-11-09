// Package cliformat formats output for the gritz CLI.
package cliformat

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/gritzapp/gritz/internal/configfile"
)

// WriteConfigEntries writes entries as a table, marking the entry named org
// (the default) with *. It never writes credentials.
func WriteConfigEntries(w io.Writer, entries []configfile.Entry, org string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\tNAME\tID\tSERVER\tLOGIN")
	for _, e := range entries {
		mark, login := "", "oauth"
		if e.Name == org {
			mark = "*"
		}
		if e.Key != "" {
			login = "key"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", mark, e.Name, e.ID, e.Server, login)
	}
	return tw.Flush()
}
