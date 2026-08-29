package cmd

import (
	"errors"
	"fmt"

	"github.com/darmawan01/interchange/ix/internal/band"
	"github.com/darmawan01/interchange/ix/internal/bufx"
	"github.com/darmawan01/interchange/ix/internal/lint"
	"github.com/spf13/cobra"
)

var errMissingAgainst = errors.New("--against is required: name the baseline, e.g. --against '.git#branch=main'")

func newLint(g *globals) *cobra.Command {
	var bandPath string
	var skipBuf bool
	c := &cobra.Command{
		Use:   "lint",
		Short: "Lint the contract and the annotations",
		Long: "Two passes. buf lint applies the STANDARD proto rules; ix then applies the\n" +
			"rules buf cannot know about:\n\n" +
			"  naming        services end in Service, fields are snake_case, ids are\n" +
			"                strings, enums have a zero UNSPECIFIED and stay append-only.\n" +
			"                These are load-bearing: the URL, the bus subject and the CLI\n" +
			"                command are derived from the names.\n" +
			"  band          every extension number in 50000-59999 must have a row in\n" +
			"                docs/annotation-band.md. Two annotations at one number is a\n" +
			"                collision the descriptor parses happily, with one of them\n" +
			"                silently gone.\n\n" +
			"Authorization is not linted unless interchange.yaml's auth block asks for\n" +
			"it: core takes no position on authz, so neither does ix.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject(g)
			if err != nil {
				return err
			}

			bufFailed := false
			if !skipBuf {
				if err := p.Buf.Run("lint"); err != nil {
					var missing *bufx.ErrNotFound
					if errors.As(err, &missing) {
						return err
					}
					bufFailed = true
				}
			}

			tbl := band.Load(p.Cfg.Root)
			if bandPath != "" {
				tbl, err = band.LoadFile(bandPath)
				if err != nil {
					return err
				}
			}
			im, err := p.Image()
			if err != nil {
				return err
			}
			onMissing := ""
			if p.Cfg.Auth != nil {
				onMissing = p.Cfg.Auth.OnMissingAnnotation
			}
			findings := lint.Run(im, lint.Options{
				Band:          tbl,
				Local:         p.Local(),
				ConfigDefault: p.Cfg.Transports.Default,
				OnMissingAuth: onMissing,
			})
			for _, f := range findings {
				fmt.Fprintln(g.ui.Out, f)
			}
			nerr := lint.Errors(findings)
			nwarn := len(findings) - nerr

			switch {
			case bufFailed || nerr > 0:
				g.ui.Fail("lint", summary(nerr, nwarn, bufFailed))
				return failed(1)
			case nwarn > 0:
				g.ui.Warn("lint", summary(nerr, nwarn, false))
			default:
				g.ui.OK("lint", fmt.Sprintf("%d RPCs, %d extensions, band %s", countRPCs(p), len(im.Extensions), tbl.Source))
			}
			return nil
		},
	}
	c.Flags().StringVar(&bandPath, "band", "", "annotation band table (default: docs/annotation-band.md, else ix's builtin copy)")
	c.Flags().BoolVar(&skipBuf, "no-buf-lint", false, "skip buf's own lint pass and run only the interchange rules")
	return c
}

func summary(nerr, nwarn int, bufFailed bool) string {
	s := fmt.Sprintf("%d error(s), %d warning(s)", nerr, nwarn)
	if bufFailed {
		s += " · buf lint reported problems above"
	}
	return s
}

func countRPCs(p *Project) int {
	ms, err := p.Methods()
	if err != nil {
		return 0
	}
	return len(ms)
}
