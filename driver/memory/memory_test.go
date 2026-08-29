package memory_test

import (
	"testing"

	"github.com/darmawan01/interchange/driver/memory"
	"github.com/darmawan01/interchange/drivertest"
)

func TestConformance(t *testing.T) {
	drivertest.Run(t, func(t *testing.T) drivertest.Pair {
		bus := memory.New()
		return drivertest.Pair{Server: bus.Driver("server"), Client: bus.Driver("client")}
	})
}

// The same driver, degraded: no native headers, no native reply, a payload
// ceiling and at-least-once delivery. Same suite, same result -- which is
// what "all variation comes from Caps()" has to mean to be worth claiming.
func TestConformanceDegraded(t *testing.T) {
	drivertest.Run(t, func(t *testing.T) drivertest.Pair {
		caps := memory.DefaultCapabilities()
		caps.NativeHeaders = false
		caps.NativeReply = false
		caps.AtLeastOnce = true
		caps.MaxPayload = 4096
		bus := memory.New(memory.WithCapabilities(caps))
		return drivertest.Pair{Server: bus.Driver("server"), Client: bus.Driver("client")}
	})
}
