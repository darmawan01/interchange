// The front end of the worked example, closing the phase-1 exit criterion
// "A front end imports its types from generated output, not a hand-written
// file".
//
// Everything below the import line is ordinary application code. The two
// things that matter are above it: CatalogService and the request/response
// types come from ../gen/ts, generated from the same
// api/catalog/v1/catalog.proto the Go server implements. Rename a field in the
// contract and `npm run typecheck` fails here -- which is the whole point, and
// the reason there is no hand-written client interface in this directory.
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  CatalogService,
  type Provider,
  ProviderStatus,
} from "../../gen/ts/catalog/v1/catalog_pb.js";

const session = () => localStorage.getItem("session") ?? "";

const transport = createConnectTransport({
  baseUrl: "/",
  interceptors: [
    // The credential is metadata, and metadata is what the chain sees. The
    // peer service on the bus sets the same key on its envelope; the authz
    // interceptor cannot tell the two apart, which is why the check cannot be
    // enforced on one road and forgotten on the other.
    (next) => async (req) => {
      req.header.set("Authorization", `Bearer ${session()}`);
      return next(req);
    },
  ],
});

const catalog = createClient(CatalogService, transport);

function label(status: ProviderStatus): string {
  switch (status) {
    case ProviderStatus.ACTIVE:
      return "active";
    case ProviderStatus.SUSPENDED:
      return "suspended";
    case ProviderStatus.UNSPECIFIED:
      return "unknown";
  }
}

function render(providers: readonly Provider[]): void {
  const list = document.getElementById("providers");
  if (list === null) {
    return;
  }
  list.replaceChildren(
    ...providers.map((p) => {
      const li = document.createElement("li");
      // providerId, displayName, status: all typed, all from the contract.
      li.textContent = `${p.displayName} (${p.providerId}) — ${label(p.status)}`;
      return li;
    }),
  );
}

function say(message: string): void {
  const status = document.getElementById("status");
  if (status !== null) {
    status.textContent = message;
  }
}

export async function load(tenantId: string): Promise<void> {
  try {
    // Fully typed, both ways. tenantId is a declared field; the response's
    // `providers` is Provider[] because the .proto said so.
    const { providers } = await catalog.listProviders({ tenantId });
    render(providers);
    say(`${providers.length} provider(s)`);
  } catch (err) {
    // The reason travels in the Ix-Reason header on this road and in the
    // envelope on the bus, and it is the same string either way -- so a client
    // branches once. connect-web surfaces the message; a production app reads
    // the header off the response and switches on the reason.
    say(err instanceof Error ? err.message : String(err));
  }
}

void load("acme");
