# The admin UI

Mockulus ships a web interface, compiled into the binary. Open the admin port in
a browser and it is there — nothing to install, nothing to deploy beside the
server, and nothing to configure to make it appear.

```sh
mockulus &
open http://localhost:9090/            # redirects to the UI
```

The address is `/__admin/mockulus/ui/`, and the admin port's root redirects to
it, so `localhost:9090` is enough. It is served on the mock port too, at the
same path, for deployments that expose only that one.

WireMock has no equivalent, so nothing here is a compatibility claim. It lives
in a namespace reserved for mockulus' own endpoints
([SPEC §5.7](../SPEC.md#57-mockulus-extensions-the-__adminmockulus-namespace)),
which WireMock answers `404` for — a client written against WireMock cannot
collide with it.

## What it does

**Stubs.** Browse and filter the registered mappings, and edit them in a JSON
editor. When the server refuses one, every problem it found is listed — not just
the first — and each one moves the cursor to the field it names. That is the
whole point of mockulus' collect-all `422`, finally somewhere you can see it:
a mapping with three unsupported fields is one round trip and three jumps rather
than three round trips.

Import and export work on the `{"mappings": [...]}` envelope. Import is atomic
on the server — one bad mapping in a batch writes nothing — and the failure says
so, and says which mapping in the batch was at fault.

**Journal.** The request log, with matched and unmatched tabs and an entry
detail that links to the stub which served it. **The journal is off by
default**, so on a fresh deployment this page tells you that and names the key
that turns it on. That is a configuration answer rather than a failure, and it
reads as one.

**Near misses.** Why a request did not match. Two ways in: from an unmatched
journal entry, or by composing a request by hand — which works with the journal
off, and is therefore the one that works on a deployment nobody has configured.
Each candidate stub is shown as a table of the criterion, what the stub asked
for, and what the request actually carried. That structured comparison is a
mockulus extension; WireMock reports near misses as prose.

**Scenarios.** A card per scenario with its current state and its possible
transitions, one click each, plus per-scenario and global reset.

**Ops.** Health, store status, snapshot epoch and stub count; a files manager
for the bodies `bodyFileName` serves; the deployment-wide settings; and the
destructive actions, behind typed confirmation.

## The token

If the deployment sets `admin_auth_token`, the UI asks for it the first time a
call is refused, and keeps it for the tab. It is held in `sessionStorage` and
nowhere else — not `localStorage`, not a cookie, not the URL. A cookie would be
attached by the browser to requests the UI never made; a URL would put the
credential in history, in the `Referer` of any link, and in every access log on
the way.

The **static assets are served without the token check**, and everything else —
including every call the UI makes — is checked as before. The reason is
mechanical: a browser cannot attach an `Authorization` header to a page load, so
a token in front of the assets makes the UI unreachable in exactly the
deployments that set one. What is exempt is code; the data behind it is not.
[SPEC §5.7](../SPEC.md#571-the-admin-ui) states the amendment in full, and one
corpus case pins both halves — assets `200` without a token while the API `401`s
without one.

## Turning it off

```sh
MOCKULUS_UI_ENABLED=false mockulus
```

The routes stop existing rather than existing and refusing: the whole prefix
answers the ordinary unsupported-endpoint `404`, and the admin port's root goes
back to answering `404` as it did before the UI existed. The admin API is
unaffected either way. See [Configuration](configuration.md).

## What it is not

**It is not exposed to the internet by anything mockulus does.** The admin
listener has no TLS ([SPEC §12.1](../SPEC.md#121-listeners) — TLS is mock-port
only), so this is an in-cluster and `kubectl port-forward` tool. Putting it in
front of anyone else is an ingress decision, and one that should come with the
token.

**It has no privileged access.** Every call it makes goes through the same
public admin API any client uses, via the published
[`@mockulus/admin-sdk`](../sdk/typescript/README.md). There are no private
endpoints behind it and no server-side session state, so anything the UI can do
is something a `curl` can do — which also means anything it cannot do is a gap
in the admin API rather than in the interface.

**It renders untrusted text.** Stub names, URLs, header values and request
bodies all arrive from whoever is driving the mock. Every field is escaped, no
part of the interface uses raw HTML interpolation, and the pages are served
under a Content-Security-Policy that admits no inline script — so an injected
`<script>` does not run even if something upstream forgot to escape it.

## Building it

The UI is a Svelte application under `ui/`, built into `internal/adminui/dist`
and embedded with `go:embed`.

```sh
make ui-build      # build the bundle
make build         # compile it into the binary
make ui-check      # type-check, lint, format, unit tests
```

**A plain `go build` with no Node installed still works.** The repository
commits a placeholder rather than a bundle, and a binary built over it serves a
page saying so and how to build one. Released binaries and container images
always carry the real interface — the Dockerfile builds it in its own stage, and
the release pipeline builds it before it packages anything.
