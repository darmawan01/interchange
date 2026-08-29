package openapi

import (
	"fmt"
	"strings"
	"unicode"
)

// The naming rules live here alone, because every one of them is a wire
// contract: rename an RPC and every client breaks, renumber a field and the
// break is silent. They are pure functions over the source text so a rule can
// be tested without a document.

// words splits an arbitrary identifier-ish string into its parts, breaking on
// anything that is not a letter or digit and on camelCase boundaries. A run of
// capitals is one word ("paymentID" -> payment, ID), so snake() produces
// payment_id rather than payment_i_d.
func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = nil
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r):
			// Break before a capital that starts a new word: either the
			// previous rune was lower/digit, or this capital is followed by a
			// lower one (the "HTTPServer" -> HTTP, Server case).
			prevLower := i > 0 && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1]))
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if prevLower || (nextLower && len(cur) > 0 && unicode.IsUpper(cur[len(cur)-1])) {
				flush()
			}
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return out
}

// camel renders words as UpperCamelCase.
func camel(s string) string {
	ws := words(s)
	var b strings.Builder
	for _, w := range ws {
		r := []rune(strings.ToLower(w))
		if len(w) > 1 && isAcronym(w) {
			// Preserve an all-caps acronym as written: ID stays ID, not Id.
			b.WriteString(w)
			continue
		}
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	return b.String()
}

func isAcronym(w string) bool {
	for _, r := range w {
		if unicode.IsLower(r) {
			return false
		}
	}
	return true
}

// snake renders words as lower_snake_case, the proto field convention.
func snake(s string) string {
	ws := words(s)
	for i, w := range ws {
		ws[i] = strings.ToLower(w)
	}
	return strings.Join(ws, "_")
}

// screaming renders words as UPPER_SNAKE_CASE, the proto enum convention.
func screaming(s string) string {
	return strings.ToUpper(snake(s))
}

// singular strips the plural suffix from one word. It is a documented
// heuristic, not English: the rules are listed in README.md, and an
// operationId or x-interchange-name overrides it whenever it guesses wrong.
func singular(s string) string {
	l := strings.ToLower(s)
	switch {
	case len(s) > 3 && strings.HasSuffix(l, "ies"):
		return s[:len(s)-3] + "y"
	case len(s) > 4 && (strings.HasSuffix(l, "ches") || strings.HasSuffix(l, "shes")):
		return s[:len(s)-2]
	case len(s) > 3 && (strings.HasSuffix(l, "ses") || strings.HasSuffix(l, "xes") || strings.HasSuffix(l, "zes")):
		return s[:len(s)-2]
	case strings.HasSuffix(l, "ss"), strings.HasSuffix(l, "us"), strings.HasSuffix(l, "is"):
		return s
	case len(s) > 1 && strings.HasSuffix(l, "s"):
		return s[:len(s)-1]
	}
	return s
}

// pathSegments splits an OpenAPI path template into its segments.
func pathSegments(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func isTemplate(seg string) bool {
	return strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")
}

// templateName returns the parameter name inside {braces}.
func templateName(seg string) string {
	return strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "}")
}

// pathParams lists the template variables of a path, in path order.
func pathParams(p string) []string {
	var out []string
	for _, seg := range pathSegments(p) {
		if isTemplate(seg) {
			out = append(out, templateName(seg))
		}
	}
	return out
}

// httpPath rewrites the OpenAPI path template so its variables name the proto
// request fields: /v1/payments/{paymentId} -> /v1/payments/{payment_id}. The
// google.api.http template is resolved against the message, so a camelCase
// variable that does not match a field name is a binding that never fires.
func httpPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if isTemplate(s) {
			segs[i] = "{" + snake(templateName(s)) + "}"
		}
	}
	return strings.Join(segs, "/")
}

// rpcName derives the RPC name for one operation. operationId wins when
// present; otherwise the method and the path shape decide. The full rule is in
// README.md -- it is part of the contract, not an implementation detail.
//
// The cases with no defensible name (POST onto an item, HEAD/OPTIONS/TRACE)
// are refused rather than given an invented one: a bad RPC name is permanent.
func rpcName(method, path string) (string, error) {
	segs := pathSegments(path)
	if len(segs) == 0 {
		return "", fmt.Errorf("path %q has no segments to derive a name from", path)
	}
	last := segs[len(segs)-1]

	if isTemplate(last) {
		if len(segs) < 2 || isTemplate(segs[len(segs)-2]) {
			return "", fmt.Errorf("path %q ends in a variable with no resource segment before it", path)
		}
		res := singular(camel(segs[len(segs)-2]))
		switch method {
		case "get":
			return "Get" + res, nil
		case "put":
			return "Replace" + res, nil
		case "patch":
			return "Update" + res, nil
		case "delete":
			return "Delete" + res, nil
		case "post":
			return "", fmt.Errorf("POST onto an item path %q has no derivable name", path)
		}
		return "", fmt.Errorf("method %q has no RPC mapping", strings.ToUpper(method))
	}

	// A non-template last segment is either a collection or a custom action.
	// A plural segment is a collection -- POST creates a member of it -- and a
	// singular one is an action on the resource before it, which is what makes
	// POST /v1/payments/{id}/refund RefundPayment and POST /v1/payments
	// CreatePayment.
	owner := ""
	for i := len(segs) - 2; i >= 0; i-- {
		if !isTemplate(segs[i]) {
			owner = segs[i]
			break
		}
	}
	plural := !strings.EqualFold(last, singular(last))

	switch method {
	case "get":
		return "List" + camel(last), nil
	case "post":
		if plural || owner == "" {
			return "Create" + singular(camel(last)), nil
		}
		return camel(last) + singular(camel(owner)), nil
	case "put":
		return "Replace" + camel(last), nil
	case "patch":
		return "Update" + camel(last), nil
	case "delete":
		return "Delete" + camel(last), nil
	}
	return "", fmt.Errorf("method %q has no RPC mapping", strings.ToUpper(method))
}

// protoIdent reports whether s is usable as a proto identifier.
func protoIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r)) {
			continue
		}
		return false
	}
	return true
}
