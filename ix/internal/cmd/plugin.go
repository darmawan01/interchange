package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/darmawan01/interchange/ix/internal/config"
	"github.com/darmawan01/interchange/ix/internal/gentmpl"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newPlugin(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "plugin",
		Short: "List, add and pin generators",
		Long: "Generators live in interchange.yaml's generate[] block. Ours have no\n" +
			"privileged standing there: a plugin you wrote is configured exactly the\n" +
			"way protocolbuffers/go is, which is the test of whether the extension\n" +
			"point is real.",
	}
	c.AddCommand(newPluginList(g), newPluginAdd(g), newPluginPin(g), newPluginRemove(g), newPluginSync(g))
	return c
}

func newPluginList(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured generators",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject(g)
			if err != nil {
				return err
			}
			if len(p.Cfg.Generate) == 0 {
				g.ui.Println("no generators configured in " + p.Rel(p.Cfg.Path))
				return nil
			}
			w := 0
			for _, gen := range p.Cfg.Generate {
				w = max(w, len(gen.Plugin))
			}
			fmt.Fprintln(g.ui.Out)
			for _, gen := range p.Cfg.Generate {
				kind := "remote"
				version := "unpinned"
				if gen.Local() {
					kind, version = "local", "built from source"
				} else if i := strings.LastIndex(gen.Plugin, ":"); i > 0 {
					version = gen.Plugin[i+1:]
				}
				extra := ""
				if gen.Strategy != "" {
					extra = " · strategy: " + gen.Strategy
				}
				fmt.Fprintf(g.ui.Out, "  %-*s  %-6s  → %s  (%s)%s\n",
					w, gen.Plugin, kind, gen.Out, version, extra)
			}
			fmt.Fprintln(g.ui.Out)
			return nil
		},
	}
}

func newPluginAdd(g *globals) *cobra.Command {
	var out, strategy string
	var opt []string
	c := &cobra.Command{
		Use:   "add <plugin>",
		Short: "Add a generator to interchange.yaml",
		Long: "The plugin is a remote reference (buf.build/org/name[:version]) or a path\n" +
			"to a local binary (./bin/protoc-gen-mysdk).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject(g)
			if err != nil {
				return err
			}
			if out == "" {
				return fmt.Errorf("--out is required: where should %s write?", args[0])
			}
			for _, gen := range p.Cfg.Generate {
				if pluginName(gen.Plugin) == pluginName(args[0]) {
					return fmt.Errorf("%s is already configured (out: %s)", gen.Plugin, gen.Out)
				}
			}
			if err := editConfig(p.Cfg.Path, func(gen *yaml.Node) error {
				// Built key by key rather than from a map: a map's iteration
				// order would put `out` before `plugin`, and the generate
				// block is read far more often than it is edited.
				n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
				put := func(k, v string) {
					n.Content = append(n.Content,
						&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k},
						&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
				}
				put("plugin", args[0])
				put("out", out)
				if len(opt) > 0 {
					seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
					for _, o := range opt {
						seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: o})
					}
					n.Content = append(n.Content,
						&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "opt"}, seq)
				}
				if strategy != "" {
					put("strategy", strategy)
				}
				gen.Content = append(gen.Content, n)
				return nil
			}); err != nil {
				return err
			}
			g.ui.OK("plugin add", fmt.Sprintf("%s → %s", args[0], out))
			return nil
		},
	}
	c.Flags().StringVar(&out, "out", "", "output directory")
	c.Flags().StringVar(&strategy, "strategy", "", "buf strategy: directory (default) or all -- anything cross-cutting needs all")
	c.Flags().StringSliceVar(&opt, "opt", nil, "plugin options")
	return c
}

func newPluginPin(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "pin <plugin> <version>",
		Short: "Pin a remote generator to a version",
		Long: "Pin every version. An unpinned generator makes the committed output a\n" +
			"function of when CI ran, which is exactly the drift the gate exists to\n" +
			"catch -- and it will catch it, noisily, on somebody else's branch.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject(g)
			if err != nil {
				return err
			}
			name, version := pluginName(args[0]), args[1]
			found := false
			if err := editConfig(p.Cfg.Path, func(gen *yaml.Node) error {
				for _, entry := range gen.Content {
					v := mapValue(entry, "plugin")
					if v == nil || pluginName(v.Value) != name {
						continue
					}
					if strings.HasPrefix(v.Value, ".") || strings.HasPrefix(v.Value, "/") {
						return fmt.Errorf("%s is a local plugin: pin it with your build, not with a version tag", v.Value)
					}
					v.Value = name + ":" + version
					v.Tag = "!!str"
					found = true
				}
				return nil
			}); err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("no generator named %s in %s", name, p.Rel(p.Cfg.Path))
			}
			g.ui.OK("plugin pin", name+":"+version)
			return nil
		},
	}
}

func newPluginRemove(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <plugin>",
		Aliases: []string{"rm"},
		Short:   "Remove a generator",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject(g)
			if err != nil {
				return err
			}
			name := pluginName(args[0])
			found := false
			if err := editConfig(p.Cfg.Path, func(gen *yaml.Node) error {
				kept := gen.Content[:0]
				for _, entry := range gen.Content {
					v := mapValue(entry, "plugin")
					if v != nil && pluginName(v.Value) == name {
						found = true
						continue
					}
					kept = append(kept, entry)
				}
				gen.Content = kept
				return nil
			}); err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("no generator named %s in %s", name, p.Rel(p.Cfg.Path))
			}
			g.ui.OK("plugin remove", name)
			return nil
		},
	}
}

func newPluginSync(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Rewrite buf.gen.yaml from interchange.yaml",
		Long: "interchange.yaml is the source of truth for which generators run. The\n" +
			"committed buf.gen.yaml exists only so `buf generate` works for someone\n" +
			"without ix installed; this regenerates it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject(g)
			if err != nil {
				return err
			}
			b, err := gentmpl.Build(p.Cfg, gentmpl.Options{})
			if err != nil {
				return err
			}
			path := filepath.Join(p.Cfg.Root, "buf.gen.yaml")
			if err := os.WriteFile(path, b, 0o644); err != nil {
				return err
			}
			g.ui.OK("plugin sync", p.Rel(path))
			return nil
		},
	}
}

// pluginName strips a version tag: "buf.build/protocolbuffers/go:v1.36.12"
// and "buf.build/protocolbuffers/go" name the same generator.
func pluginName(s string) string {
	if strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/") {
		return s
	}
	i := strings.LastIndex(s, ":")
	j := strings.LastIndex(s, "/")
	if i > j {
		return s[:i]
	}
	return s
}

// editConfig rewrites interchange.yaml through a yaml.Node round-trip, which
// keeps the user's comments and key order. A config file that loses its
// comments every time a tool touches it is a config file people stop
// commenting.
func editConfig(path string, edit func(generate *yaml.Node) error) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmt.Errorf("%s: not a YAML document", path)
	}
	root := doc.Content[0]
	gen := mapValue(root, "generate")
	if gen == nil {
		gen = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "generate"}, gen)
	}
	if gen.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s: generate must be a list", path)
	}
	if err := edit(gen); err != nil {
		return err
	}

	out, err := marshalDoc(&doc)
	if err != nil {
		return err
	}
	// Parse the result before overwriting: a tool that writes a config it
	// cannot read back has broken the project rather than edited it.
	if _, err := config.ParseBytes(out, path); err != nil {
		return fmt.Errorf("refusing to write %s: the edit produced an invalid config: %w", path, err)
	}
	return os.WriteFile(path, out, 0o644)
}

func marshalDoc(doc *yaml.Node) ([]byte, error) {
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
