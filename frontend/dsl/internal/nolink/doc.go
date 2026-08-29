// Package nolink exists to link nothing.
//
// The DSL frontend no longer imports the optional modules' generated Go: a
// frontend that linked /auth in to emit an auth annotation would make the
// optional module mandatory. The annotation descriptors arrive in
// Options.Deps instead. That property is invisible in the frontend's own
// tests, because those link the generated types to assert on them -- so it is
// tested here, from a package whose import graph contains neither.
package nolink
