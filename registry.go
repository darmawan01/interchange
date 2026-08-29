package interchange

import (
	"fmt"
	"slices"
	"sync"
)

// DriverFactory builds a driver from its configuration block in
// interchange.yaml. Registering one is how a broker adapter becomes available
// to `ix` and to a server that wires drivers by name.
type DriverFactory func(cfg map[string]string) (Driver, error)

var drivers = struct {
	sync.RWMutex
	m map[string]DriverFactory
}{m: map[string]DriverFactory{}}

// RegisterDriver makes a driver available by name ("nats", "mqtt", "ws").
func RegisterDriver(name string, f DriverFactory) {
	drivers.Lock()
	defer drivers.Unlock()
	drivers.m[name] = f
}

// NewDriver builds a registered driver.
func NewDriver(name string, cfg map[string]string) (Driver, error) {
	drivers.RLock()
	f, ok := drivers.m[name]
	drivers.RUnlock()
	if !ok {
		return nil, fmt.Errorf("interchange: no driver registered as %q (have %v)", name, Drivers())
	}
	return f(cfg)
}

// Drivers lists the registered driver names, sorted.
func Drivers() []string {
	drivers.RLock()
	defer drivers.RUnlock()
	out := make([]string, 0, len(drivers.m))
	for n := range drivers.m {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}
