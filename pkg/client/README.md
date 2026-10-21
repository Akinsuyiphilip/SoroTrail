# pkg/client — the SoroTrail API client

`pkg/client` is the versioned Go client for the SoroTrail HTTP API. It is
**generated** from [`api/openapi.yaml`](../../api/openapi.yaml), the source
of truth for the API, by [`cmd/clientgen`](../../cmd/clientgen):

```sh
make client   # regenerates pkg/client/client.gen.go from api/openapi.yaml
```

The generated surface (`client.gen.go`) holds:

- one Go type per `components/schemas` entry;
- one typed method per documented operation, e.g.
  `Client.ListEvents(ctx, ListEventsParams)` or `Client.GetEvent(ctx, id, GetEventParams)`;
- `SpecVersion` (the spec's `info.version` the client was generated from)
  and `SpecRoutes` (every documented route).

`pkg/client/drift_test.go` regenerates the client from the committed spec
and compares byte-for-byte against the committed file, so a spec change
that is not followed by `make client` fails CI instead of drifting.
`client_test.go` exercises the public client against `httptest` in the
default test suite, including request construction, pagination, API
errors, authentication, and context cancellation. The database-backed
real-server test in `client_integration_test.go` remains available with
the `integration` build tag.

## Usage

```go
import "github.com/sorotrail/sorotrail/pkg/client"

c := client.New("https://sorotrail.example.com", client.WithAPIKey("st_..."))

page, err := c.ListEvents(ctx, client.ListEventsParams{
    ContractID: contractID,
    Limit:      50,
})
```

Non-2xx responses surface as `*client.APIError` carrying the server's
error envelope.

## Versioning

The client is versioned in lockstep with the API spec: `SpecVersion`
carries the `info.version` of the `api/openapi.yaml` it was generated
from, and release automation bumps that version with the API. See
[`RELEASING.md`](../../RELEASING.md) for how releases publish the client.

## Regenerating after a spec change

```sh
# 1. edit api/openapi.yaml
make spec      # refresh the served openapi.json copy
make client    # regenerate pkg/client/client.gen.go
go test ./pkg/client/...
```

Both files are committed; the drift tests in `pkg/docs` and `pkg/client`
fail the build until `make spec` / `make client` are run.
