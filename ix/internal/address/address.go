// Package address derives what an RPC looks like on each road.
//
// The derivations are the ones documented in docs/04-bindings.md, written
// out here rather than imported from the driver modules: `ix` is one binary a
// user installs, and it must describe a NATS subject without linking a NATS
// client. A driver that disagrees with this table is a driver that broke the
// documented address grammar.
package address

import (
	"fmt"
	"strings"
)

// MQTTPrefix is the topic root drivers namespace under. docs/04 writes it as
// `org/rpc/Svc/Method`; the org segment is deployment configuration, so ix
// prints the project-neutral default and a driver overrides it.
const MQTTPrefix = "interchange"

// Procedure is the Connect-style procedure string for a service and method.
func Procedure(service, method string) string { return "/" + service + "/" + method }

// RPC is the HTTP address: Connect posts to the procedure path.
func RPC(service, method string) string { return "POST " + Procedure(service, method) }

// Bus is the NATS subject: "/pkg.Svc/Method" becomes "rpc.pkg.Svc.Method".
// This is driver/memory's Address, which is the reference implementation.
func Bus(service, method string) string { return "rpc." + service + "." + method }

// MQTT is the MQTT 5 topic.
func MQTT(service, method string) string {
	return MQTTPrefix + "/rpc/" + service + "/" + method
}

// WS is the WebSocket address. There is only one channel, so the procedure
// lives entirely in the envelope -- the degenerate case, not the hard one.
func WS(service, method string) string { return "one socket · procedure in the envelope" }

// Caps is what a road can honestly claim, for the line `ix describe` prints
// under an exposed message transport. Zero values mean "not known here" and
// print as such rather than as a confident wrong answer.
type Caps struct {
	CompetingGroup bool
	AtLeastOnce    string // "yes", "no", or "" for unknown
	MaxPayload     string // human form, or "" for unknown
}

// Known is the per-road capability defaults from docs/04's matrix. They are
// defaults: a deployment tunes them, and a driver reports the truth at
// runtime through Capabilities.
var Known = map[string]Caps{
	"bus":  {CompetingGroup: true, AtLeastOnce: "no", MaxPayload: "1 MiB"},
	"mqtt": {CompetingGroup: true, AtLeastOnce: "yes", MaxPayload: "256 MiB"},
	"ws":   {CompetingGroup: false, AtLeastOnce: "no"},
}

// Detail is the second line under a message transport: queue group,
// at-least-once and max payload, omitting whatever is not known.
func Detail(road, group string) string {
	c, ok := Known[road]
	if !ok {
		return ""
	}
	var parts []string
	switch {
	case !c.CompetingGroup:
		parts = append(parts, "queue group: n/a")
	case group == "":
		parts = append(parts, "queue group: none")
	default:
		parts = append(parts, "queue group: "+group)
	}
	if c.AtLeastOnce != "" {
		parts = append(parts, "at-least-once: "+c.AtLeastOnce)
	}
	if c.MaxPayload != "" {
		parts = append(parts, "max payload: "+c.MaxPayload)
	}
	return strings.Join(parts, " · ")
}

// For returns the address of a method on one road, and whether that road
// carries a detail line.
func For(road, service, method, httpMethod, httpPath string) string {
	switch road {
	case "rpc":
		return RPC(service, method)
	case "rest":
		if httpPath == "" {
			return "no (google.api.http) rule"
		}
		return fmt.Sprintf("%-4s %s", httpMethod, httpPath)
	case "bus":
		return Bus(service, method)
	case "mqtt":
		return MQTT(service, method)
	case "ws":
		return WS(service, method)
	}
	return ""
}
