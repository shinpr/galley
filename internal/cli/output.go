package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func renderOutput(cmd *cobra.Command, output string, payload any, renderText func() error) error {
	switch output {
	case "json":
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	case "text":
		return renderText()
	default:
		return fmt.Errorf("unsupported output format %q", output)
	}
}
