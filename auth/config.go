package auth

import (
	"fmt"
	"log/slog"
	"slices"
	"sync"
)

// Strictness is what the module does with an RPC that carries no (auth)
// annotation. It is a property of this module, configured by the adopter --
// core has no such setting because core never looks at the annotation.
type Strictness string

// The three policies. The zero value resolves to StrictError: a config that
// forgot to say denies rather than opens.
const (
	// StrictError denies the call. The default, because a half-annotated
	// service is worse than an unannotated one -- the reviewer believes the
	// annotations mean something.
	StrictError Strictness = "error"

	// StrictWarn allows the call and logs it. A migration state, not a
	// destination.
	StrictWarn Strictness = "warn"

	// StrictIgnore allows the call silently. For a tree being annotated
	// incrementally where the log noise is not yet useful.
	StrictIgnore Strictness = "ignore"
)

// ParseStrictness parses the on_missing_annotation config value.
func ParseStrictness(s string) (Strictness, error) {
	switch Strictness(s) {
	case "", StrictError:
		return StrictError, nil
	case StrictWarn:
		return StrictWarn, nil
	case StrictIgnore:
		return StrictIgnore, nil
	}
	return "", fmt.Errorf("auth: on_missing_annotation is %q, want error, warn or ignore", s)
}

// Provider names the decider a deployment wants. rbac ships here; opa, cedar
// and anything else are registered by whoever brings them, which is the point
// -- the Authorizer interface is the whole contract.
type Provider string

// The providers this module knows by name.
const (
	ProviderRBAC   Provider = "rbac"
	ProviderOPA    Provider = "opa"
	ProviderCedar  Provider = "cedar"
	ProviderCustom Provider = "custom"
)

// Config mirrors the authz block of interchange.yaml:
//
//	authz:
//	  provider: rbac              # or opa | cedar | custom
//	  on_missing_annotation: error   # error | warn | ignore
//
// The zero value is usable and strict.
type Config struct {
	// Provider selects the registered authorizer NewAuthorizer builds. It is
	// ignored when you construct an Authorizer yourself and hand it to Authz.
	Provider Provider `yaml:"provider" json:"provider"`

	// OnMissingAnnotation is the strictness policy. Empty means StrictError.
	OnMissingAnnotation Strictness `yaml:"on_missing_annotation" json:"on_missing_annotation"`

	// Options configures the provider. RBAC reads role -> comma-separated
	// atoms; another provider reads whatever it documents.
	Options map[string]string `yaml:"options" json:"options"`

	// Logger receives the warn policy's warnings. Nil means slog.Default().
	Logger *slog.Logger `yaml:"-" json:"-"`
}

// Strictness resolves the configured policy, defaulting to StrictError.
func (c Config) Strictness() Strictness {
	s, err := ParseStrictness(string(c.OnMissingAnnotation))
	if err != nil {
		// An invalid value is a config bug; Validate reports it. Denying is
		// the only safe reading of "I could not tell what you meant".
		return StrictError
	}
	return s
}

func (c Config) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// Validate reports a config that would not do what it says.
func (c Config) Validate() error {
	if _, err := ParseStrictness(string(c.OnMissingAnnotation)); err != nil {
		return err
	}
	if c.Provider == "" {
		return nil
	}
	if c.Provider != ProviderCustom && !hasProvider(c.Provider) {
		return fmt.Errorf("auth: no authorizer registered as %q (have %s)", c.Provider, joinProviders())
	}
	return nil
}

// AuthorizerFactory builds an Authorizer from a provider's config block.
// Registering one is how an OPA or Cedar adapter becomes available by name
// without this module importing a policy engine.
type AuthorizerFactory func(options map[string]string) (Authorizer, error)

var providers = struct {
	sync.RWMutex
	m map[Provider]AuthorizerFactory
}{m: map[Provider]AuthorizerFactory{}}

// RegisterProvider makes an Authorizer available under a config name.
func RegisterProvider(name Provider, f AuthorizerFactory) {
	providers.Lock()
	defer providers.Unlock()
	providers.m[name] = f
}

// Providers lists the registered provider names, sorted.
func Providers() []Provider {
	providers.RLock()
	defer providers.RUnlock()
	out := make([]Provider, 0, len(providers.m))
	for n := range providers.m {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func hasProvider(name Provider) bool {
	providers.RLock()
	defer providers.RUnlock()
	_, ok := providers.m[name]
	return ok
}

func joinProviders() string {
	names := Providers()
	strs := make([]string, len(names))
	for i, n := range names {
		strs[i] = string(n)
	}
	if len(strs) == 0 {
		return "none"
	}
	return fmt.Sprint(strs)
}

// NewAuthorizer builds the configured provider.
//
// ProviderCustom is deliberately not buildable from config: a bespoke decider
// is a Go value you construct and pass to Authz, not a name in a YAML file.
func NewAuthorizer(cfg Config) (Authorizer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch cfg.Provider {
	case "":
		return nil, fmt.Errorf("auth: config names no provider")
	case ProviderCustom:
		return nil, fmt.Errorf("auth: provider %q is constructed in Go and passed to Authz, not built from config", ProviderCustom)
	}
	providers.RLock()
	f := providers.m[cfg.Provider]
	providers.RUnlock()
	if f == nil {
		return nil, fmt.Errorf("auth: no authorizer registered as %q (have %s); register one with auth.RegisterProvider", cfg.Provider, joinProviders())
	}
	return f(cfg.Options)
}
