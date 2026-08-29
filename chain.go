package interchange

import (
	"fmt"
	"slices"
)

// Stage is a named interceptor. Names exist so a chain can be extended by
// anchor rather than by position: a positional chain breaks silently the day
// a stage is inserted upstream, a named one fails loudly if the anchor
// disappears.
type Stage struct {
	Name        string
	Interceptor Interceptor
}

// Named pairs a bare Interceptor with the name a chain refers to it by.
func Named(name string, i Interceptor) Stage { return Stage{Name: name, Interceptor: i} }

// ChainSpec is an ordered, named list of interceptors. Stage 0 is outermost:
// it sees the request first and the response last.
//
// A ChainSpec is immutable. Every combinator returns a new value, so handing
// the same chain to two bindings cannot let one of them mutate the other's --
// which is half of why chain symmetry holds.
type ChainSpec struct {
	stages []Stage
	err    error
}

// Chain builds a chain from stages, in order. An empty chain is valid.
func Chain(stages ...Stage) *ChainSpec {
	c := &ChainSpec{stages: slices.Clone(stages)}
	c.err = validateStages(c.stages)
	return c
}

func validateStages(stages []Stage) error {
	seen := make(map[string]struct{}, len(stages))
	for _, s := range stages {
		if s.Name == "" {
			return fmt.Errorf("interchange: chain stage has no name (use interchange.Named)")
		}
		if s.Interceptor == nil {
			return fmt.Errorf("interchange: chain stage %q has a nil interceptor", s.Name)
		}
		if _, dup := seen[s.Name]; dup {
			return fmt.Errorf("interchange: chain stage %q appears twice", s.Name)
		}
		seen[s.Name] = struct{}{}
	}
	return nil
}

func (c *ChainSpec) with(stages []Stage, err error) *ChainSpec {
	next := &ChainSpec{stages: stages, err: c.err}
	if next.err == nil {
		next.err = err
	}
	if next.err == nil {
		next.err = validateStages(stages)
	}
	return next
}

func (c *ChainSpec) indexOf(anchor string) int {
	return slices.IndexFunc(c.stages, func(s Stage) bool { return s.Name == anchor })
}

// After inserts stages immediately after the named anchor.
func (c *ChainSpec) After(anchor string, stages ...Stage) *ChainSpec {
	i := c.indexOf(anchor)
	if i < 0 {
		return c.with(c.stages, fmt.Errorf("interchange: chain has no stage %q to insert after", anchor))
	}
	return c.with(slices.Insert(slices.Clone(c.stages), i+1, stages...), nil)
}

// Before inserts stages immediately before the named anchor.
func (c *ChainSpec) Before(anchor string, stages ...Stage) *ChainSpec {
	i := c.indexOf(anchor)
	if i < 0 {
		return c.with(c.stages, fmt.Errorf("interchange: chain has no stage %q to insert before", anchor))
	}
	return c.with(slices.Insert(slices.Clone(c.stages), i, stages...), nil)
}

// Replace swaps the named stage for another, keeping its position.
func (c *ChainSpec) Replace(anchor string, stage Stage) *ChainSpec {
	i := c.indexOf(anchor)
	if i < 0 {
		return c.with(c.stages, fmt.Errorf("interchange: chain has no stage %q to replace", anchor))
	}
	next := slices.Clone(c.stages)
	next[i] = stage
	return c.with(next, nil)
}

// Remove drops the named stage.
func (c *ChainSpec) Remove(anchor string) *ChainSpec {
	i := c.indexOf(anchor)
	if i < 0 {
		return c.with(c.stages, fmt.Errorf("interchange: chain has no stage %q to remove", anchor))
	}
	return c.with(slices.Delete(slices.Clone(c.stages), i, i+1), nil)
}

// Append adds stages at the innermost end, closest to the handler.
func (c *ChainSpec) Append(stages ...Stage) *ChainSpec {
	return c.with(append(slices.Clone(c.stages), stages...), nil)
}

// Prepend adds stages at the outermost end, before everything else.
func (c *ChainSpec) Prepend(stages ...Stage) *ChainSpec {
	return c.with(slices.Insert(slices.Clone(c.stages), 0, stages...), nil)
}

// Names lists the stage names, outermost first. This is the value a chain
// symmetry test compares across bindings.
func (c *ChainSpec) Names() []string {
	out := make([]string, len(c.stages))
	for i, s := range c.stages {
		out[i] = s.Name
	}
	return out
}

// Len reports the number of stages.
func (c *ChainSpec) Len() int { return len(c.stages) }

// Err reports the first error accumulated by a combinator -- a missing
// anchor, a duplicate name, a nil interceptor. Register returns it rather
// than silently serving a chain that is not the one you asked for.
func (c *ChainSpec) Err() error { return c.err }

// Wrap folds the chain around final. Stage 0 ends up outermost.
func (c *ChainSpec) Wrap(final UnaryFunc) (UnaryFunc, error) {
	if c == nil {
		return final, nil
	}
	if c.err != nil {
		return nil, c.err
	}
	next := final
	for i := len(c.stages) - 1; i >= 0; i-- {
		next = c.stages[i].Interceptor(next)
	}
	return next, nil
}

// MustWrap is Wrap for wiring code that would only panic on the next line
// anyway.
func (c *ChainSpec) MustWrap(final UnaryFunc) UnaryFunc {
	f, err := c.Wrap(final)
	if err != nil {
		panic(err)
	}
	return f
}
