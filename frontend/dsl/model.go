package dsl

import "gopkg.in/yaml.v3"

// The parsed DSL, one struct per proto construct. Every node reference is
// kept so a later validation failure still knows where in the YAML it came
// from -- diagnostics are produced after resolution, not during parsing.

type file struct {
	path      string
	pkg       string
	pkgNode   *yaml.Node
	goPackage string
	baseName  string // emitted .proto file name, without extension
	doc       string

	messages []*message
	enums    []*enum
	services []*service
}

type message struct {
	name string
	node *yaml.Node
	doc  string

	fields   []*field
	messages []*message
	enums    []*enum
}

type field struct {
	name     string
	node     *yaml.Node // the field name key
	typeNode *yaml.Node
	numNode  *yaml.Node

	typ      string
	num      int
	repeated bool
	optional bool
	doc      string

	// mapKey and mapVal are set when typ was map<k, v>.
	isMap  bool
	mapKey string
	mapVal string

	// rendered is the proto type expression, filled in by resolve.
	rendered string
}

type enum struct {
	name   string
	node   *yaml.Node
	doc    string
	values []*enumValue
}

type enumValue struct {
	name string
	node *yaml.Node
	num  int
}

type service struct {
	name string
	node *yaml.Node
	doc  string

	annot *annotations // service-level: only transports and group are read
	rpcs  []*rpc
}

type rpc struct {
	name     string
	node     *yaml.Node
	doc      string
	request  string
	reqNode  *yaml.Node
	response string
	respNode *yaml.Node

	annot *annotations
}

// annotations is the whole interchange annotation vocabulary, in one struct,
// used identically for the inline form and the sidecar form. One vocabulary,
// two places: a team that starts inline and later moves to a sidecar does not
// relearn anything.
type annotations struct {
	// origin distinguishes the two homes in a conflict diagnostic.
	origin string

	// procNode is the sidecar's procedure key, for diagnostics that have no
	// more specific node.
	procNode *yaml.Node

	transports     []string
	transportsNode *yaml.Node
	group          string

	http     *httpRule
	httpNode *yaml.Node

	auth     *authRule
	authNode *yaml.Node

	cli     *cliRule
	cliNode *yaml.Node

	internal     bool
	internalSet  bool
	internalNode *yaml.Node

	idempotency     string
	idempotencyNode *yaml.Node
}

type httpRule struct {
	method string // get, post, put, patch, delete
	path   string
	body   string
}

type authRule struct {
	authTypes []string
	resource  string
	verb      string
	hasPerm   bool
	public    bool
	platform  bool
}

type cliRule struct {
	path  []string
	args  []string
	short string
	long  string
	skip  bool
}
