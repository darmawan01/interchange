package cmd

import (
	"github.com/spf13/cobra"
)

func newFmt(g *globals) *cobra.Command {
	var check bool
	c := &cobra.Command{
		Use:   "fmt",
		Short: "Format proto sources in place",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject(g)
			if err != nil {
				return err
			}
			args2 := []string{"format", "-w"}
			if check {
				args2 = []string{"format", "--diff", "--exit-code"}
			}
			for _, d := range p.Cfg.ProtoDirs() {
				if err := p.Buf.Run(append(args2, d)...); err != nil {
					return failed(1)
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&check, "check", false, "report unformatted files instead of rewriting them (for CI)")
	return c
}

func newBreaking(g *globals) *cobra.Command {
	var against string
	c := &cobra.Command{
		Use:   "breaking",
		Short: "Detect breaking changes against a git ref or a registry",
		Long: "Runs buf breaking with the project's configured rule. The interchange\n" +
			"default is WIRE_JSON rather than FILE: it allows refactors that keep both\n" +
			"the binary wire form and the JSON field names compatible, which is what a\n" +
			"public JSON surface actually requires (docs/02).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject(g)
			if err != nil {
				return err
			}
			if against == "" {
				return fail(2, errMissingAgainst)
			}
			if err := p.Buf.Run("breaking", "--against", against); err != nil {
				return failed(1)
			}
			p.UI.OK("breaking", "no incompatible change against "+against)
			return nil
		},
	}
	c.Flags().StringVar(&against, "against", "", "the baseline: a git ref (\".git#branch=main\"), a directory, or a registry reference")
	return c
}
