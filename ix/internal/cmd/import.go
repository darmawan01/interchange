package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// `ix import` is the on-ramp: a non-proto source becomes part of the
// canonical tree. The frontend adapters that do the conversion live in
// their own modules (docs/09); this build of ix ships none of them, so
// import detects the format, names the frontend that would read it, and
// stops.
//
// It stops rather than guessing on purpose. A frontend that silently drops
// what it cannot represent produces a contract that lies, which is the exact
// failure this project exists to remove -- so a partial import is worse than
// no import.
func newImport(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "import <file>",
		Short: "Bring a non-proto source into the canonical tree",
		Long: "Detects the source format and names the frontend that reads it.\n\n" +
			"No frontend adapter is wired into this build, so nothing is written. When\n" +
			"one is, it will refuse to emit a partial contract: every construct it\n" +
			"cannot represent is reported with its source location, and the import\n" +
			"produces nothing until they are resolved.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			kind, frontend := detectFormat(path, b)
			fmt.Fprintln(g.ui.Out)
			fmt.Fprintf(g.ui.Out, "  detected   %s\n", kind)
			fmt.Fprintf(g.ui.Out, "  frontend   %s\n", frontend)
			fmt.Fprintln(g.ui.Out)
			if frontend == "unknown" {
				return fail(2, fmt.Errorf("%s: unrecognised source format", path))
			}
			fmt.Fprintf(g.ui.Out, "  nothing written — the %s frontend is not wired into this build of ix\n", frontend)
			fmt.Fprintln(g.ui.Out)
			return failed(3)
		},
	}
}

// detectFormat sniffs the source rather than trusting the extension: a
// .yaml file is OpenAPI, an AsyncAPI document or somebody's config, and the
// difference is in the first key.
func detectFormat(path string, b []byte) (kind, frontend string) {
	head := string(b)
	if len(head) > 4096 {
		head = head[:4096]
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case ext == ".proto":
		return "protobuf", "proto"
	case strings.Contains(head, "openapi:") || strings.Contains(head, `"openapi"`):
		return "OpenAPI " + versionAfter(head, "openapi"), "openapi"
	case strings.Contains(head, "swagger:") || strings.Contains(head, `"swagger"`):
		return "Swagger " + versionAfter(head, "swagger"), "openapi"
	case strings.Contains(head, "asyncapi:") || strings.Contains(head, `"asyncapi"`):
		return "AsyncAPI " + versionAfter(head, "asyncapi"), "asyncapi"
	case strings.Contains(head, "$schema"):
		return "JSON Schema", "jsonschema"
	case ext == ".graphql" || ext == ".gql" || strings.Contains(head, "type Query"):
		return "GraphQL SDL", "graphql"
	case ext == ".tsp":
		return "TypeSpec", "typespec"
	case ext == ".ix":
		return "Interchange DSL", "dsl"
	}
	return "unrecognised", "unknown"
}

func versionAfter(head, key string) string {
	i := strings.Index(head, key)
	if i < 0 {
		return ""
	}
	rest := head[i+len(key):]
	rest = strings.TrimLeft(rest, `":' `)
	end := strings.IndexAny(rest, "\r\n\",' ")
	if end < 0 {
		end = len(rest)
	}
	return strings.TrimSpace(rest[:end])
}
