package openapi

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/darmawan01/interchange"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
)

func TestWordSplitting(t *testing.T) {
	cases := []struct{ in, camel, snake, screaming string }{
		{"paymentId", "PaymentId", "payment_id", "PAYMENT_ID"},
		{"payment_id", "PaymentId", "payment_id", "PAYMENT_ID"},
		{"paymentID", "PaymentID", "payment_id", "PAYMENT_ID"},
		{"payment-methods", "PaymentMethods", "payment_methods", "PAYMENT_METHODS"},
		{"HTTPServer", "HTTPServer", "http_server", "HTTP_SERVER"},
		{"listPayments", "ListPayments", "list_payments", "LIST_PAYMENTS"},
		{"payment.created", "PaymentCreated", "payment_created", "PAYMENT_CREATED"},
		{"v1", "V1", "v1", "V1"},
		{"last4", "Last4", "last4", "LAST4"},
	}
	for _, tc := range cases {
		if got := camel(tc.in); got != tc.camel {
			t.Errorf("camel(%q) = %q, want %q", tc.in, got, tc.camel)
		}
		if got := snake(tc.in); got != tc.snake {
			t.Errorf("snake(%q) = %q, want %q", tc.in, got, tc.snake)
		}
		if got := screaming(tc.in); got != tc.screaming {
			t.Errorf("screaming(%q) = %q, want %q", tc.in, got, tc.screaming)
		}
	}
}

func TestSingular(t *testing.T) {
	cases := map[string]string{
		"providers":       "provider",
		"policies":        "policy",
		"addresses":       "address",
		"batches":         "batch",
		"payment-methods": "payment-method",
		"status":          "status",
		"analysis":        "analysis",
		"access":          "access",
		"refund":          "refund",
	}
	for in, want := range cases {
		if got := singular(in); got != want {
			t.Errorf("singular(%q) = %q, want %q", in, got, want)
		}
	}
}

// The naming rule is part of the contract: an RPC name is what every client
// calls, so it is pinned here rather than left to whatever the code does.
func TestRPCName(t *testing.T) {
	cases := []struct {
		method, path, want string
	}{
		{"get", "/v1/providers", "ListProviders"},
		{"post", "/v1/providers", "CreateProvider"},
		{"get", "/v1/providers/{id}", "GetProvider"},
		{"put", "/v1/providers/{id}", "ReplaceProvider"},
		{"patch", "/v1/providers/{id}", "UpdateProvider"},
		{"delete", "/v1/providers/{id}", "DeleteProvider"},
		{"post", "/v1/payments/{id}/refund", "RefundPayment"},
		{"post", "/v1/reports/{id}/re-run", "ReRunReport"},
		{"get", "/v1/customers/{id}/payment-methods", "ListPaymentMethods"},
		{"post", "/v1/customers/{id}/payment-methods", "CreatePaymentMethod"},
		{"delete", "/v1/customers/{id}/payment-methods", "DeletePaymentMethods"},
		{"post", "/v1/payments/search", "SearchPayment"},
		{"post", "/v1/webhooks/replay", "ReplayWebhook"},
		{"get", "/v1/policies", "ListPolicies"},
		{"get", "/v1/policies/{id}", "GetPolicy"},
	}
	for _, tc := range cases {
		got, err := rpcName(tc.method, tc.path)
		if err != nil {
			t.Errorf("rpcName(%s %s): %v", tc.method, tc.path, err)
			continue
		}
		if got != tc.want {
			t.Errorf("rpcName(%s %s) = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}

	refused := []struct{ method, path string }{
		{"post", "/v1/providers/{id}"},
		{"get", "/"},
		{"get", "/{id}"},
		{"options", "/v1/providers"},
	}
	for _, tc := range refused {
		if got, err := rpcName(tc.method, tc.path); err == nil {
			t.Errorf("rpcName(%s %s) = %q, want a refusal", tc.method, tc.path, got)
		}
	}
}

func TestHTTPPathRewrite(t *testing.T) {
	cases := map[string]string{
		"/v1/payments":                       "/v1/payments",
		"/v1/payments/{paymentId}":           "/v1/payments/{payment_id}",
		"/v1/customers/{customerId}/methods": "/v1/customers/{customer_id}/methods",
		"/v1/a/{aId}/b/{bID}":                "/v1/a/{a_id}/b/{b_id}",
	}
	for in, want := range cases {
		if got := httpPath(in); got != want {
			t.Errorf("httpPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// google.api.http is the one annotation an import must never ask the user to
// restate, so the derivation is asserted end to end: through the emitted
// proto, the compiler, and back off the descriptor.
func TestDerivedHTTPRules(t *testing.T) {
	cases := []struct {
		method, path string
		rpc          string
		verb         string
		httpPath     string
		body         string
	}{
		{"get", "/v1/providers", "ListProviders", "get", "/v1/providers", ""},
		{"post", "/v1/providers", "CreateProvider", "post", "/v1/providers", "*"},
		{"get", "/v1/providers/{providerId}", "GetProvider", "get", "/v1/providers/{provider_id}", ""},
		{"patch", "/v1/providers/{providerId}", "UpdateProvider", "patch", "/v1/providers/{provider_id}", "*"},
		{"put", "/v1/providers/{providerId}", "ReplaceProvider", "put", "/v1/providers/{provider_id}", "*"},
		{"delete", "/v1/providers/{providerId}", "DeleteProvider", "delete", "/v1/providers/{provider_id}", ""},
		{"post", "/v1/providers/{providerId}/sync", "SyncProvider", "post", "/v1/providers/{provider_id}/sync", "*"},
		{"get", "/v1/user-profiles/{userProfileId}", "GetUserProfile", "get", "/v1/user-profiles/{user_profile_id}", ""},
	}

	var b strings.Builder
	b.WriteString("openapi: 3.0.3\ninfo: {title: Routes, version: \"1\"}\nx-interchange-auth: {public: true}\npaths:\n")
	seen := map[string]bool{}
	for _, tc := range cases {
		if !seen[tc.path] {
			seen[tc.path] = true
			fmt.Fprintf(&b, "  %s:\n", tc.path)
			for _, v := range pathParams(tc.path) {
				fmt.Fprintf(&b, "    parameters: [{name: %s, in: path, required: true, schema: {type: string}}]\n", v)
			}
		}
		fmt.Fprintf(&b, "    %s:\n", tc.method)
		switch tc.method {
		case "post", "put", "patch":
			b.WriteString("      requestBody:\n        content:\n          application/json:\n" +
				"            schema: {type: object, properties: {note: {type: string}}}\n")
		}
		b.WriteString("      responses:\n        \"204\": {description: ok}\n")
	}

	res, err := (&Frontend{}).Import(context.Background(), interchange.Sources{
		Paths:   []string{"routes.yaml"},
		Content: map[string][]byte{"routes.yaml": []byte(b.String())},
	}, interchange.Options{Package: "routes.v1", Params: map[string]string{"service": "RoutesService"}})
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, render(res.Diagnostics), b.String())
	}
	svc := findService(t, res.Files, "routes/v1/routes_service.proto", "RoutesService")
	for _, tc := range cases {
		m := findMethod(t, svc, tc.rpc)
		rule, _ := proto.GetExtension(m.GetOptions(), annotations.E_Http).(*annotations.HttpRule)
		var verb, p string
		switch pattern := rule.GetPattern().(type) {
		case *annotations.HttpRule_Get:
			verb, p = "get", pattern.Get
		case *annotations.HttpRule_Post:
			verb, p = "post", pattern.Post
		case *annotations.HttpRule_Put:
			verb, p = "put", pattern.Put
		case *annotations.HttpRule_Patch:
			verb, p = "patch", pattern.Patch
		case *annotations.HttpRule_Delete:
			verb, p = "delete", pattern.Delete
		}
		if verb != tc.verb || p != tc.httpPath || rule.GetBody() != tc.body {
			t.Errorf("%s: http = %s %q body %q, want %s %q body %q",
				tc.rpc, verb, p, rule.GetBody(), tc.verb, tc.httpPath, tc.body)
		}
	}
}

// operationId and x-interchange-name both override the derived name, in that
// order of specificity.
func TestNameOverrides(t *testing.T) {
	doc := head + `
  /v1/things:
    get:
      operationId: enumerateThings
      x-interchange-auth: {public: true}
      responses:
        "204": {description: ok}
  /v1/other-things:
    get:
      operationId: ignored
      x-interchange-name: FetchOtherThings
      x-interchange-auth: {public: true}
      responses:
        "204": {description: ok}
`
	res, err := (&Frontend{}).Import(context.Background(), interchange.Sources{
		Paths:   []string{"doc.yaml"},
		Content: map[string][]byte{"doc.yaml": []byte(doc)},
	}, interchange.Options{Package: "things.v1", Params: map[string]string{"service": "ThingsService"}})
	if err != nil {
		t.Fatalf("%v\n%s", err, render(res.Diagnostics))
	}
	svc := findService(t, res.Files, "things/v1/things_service.proto", "ThingsService")
	findMethod(t, svc, "EnumerateThings")
	findMethod(t, svc, "FetchOtherThings")
}
