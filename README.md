# Atlas Bridge

Atlas Bridge is a small, private catalogue gateway for [Kavita Atlas](https://github.com/ElHefe3/kavita-atlas). It presents unstable upstream catalogues through one normalized, authenticated HTTP API and proxies covers and book streams so Atlas never follows provider-controlled CDN URLs.

The first built-in adapters are Anna's Archive and Library Genesis. Use them only for material you are legally permitted to access. Atlas Bridge does not include content, credentials, a browser engine, arbitrary scripts, dynamic modules, or executable provider definitions.

## API

All `/v1` routes require `Authorization: Bearer <bridge token>`. `GET /healthz` is unauthenticated for container health checks. The complete contract is in [openapi.yaml](openapi.yaml).

```text
GET /v1/providers
GET /v1/providers/{provider}/search?q=&page=&pageSize=&format=
GET /v1/providers/{provider}/books/{externalId}
GET /v1/providers/{provider}/books/{externalId}/cover
GET /v1/providers/{provider}/books/{externalId}/files/{fileId}
```

Upstream URLs are never present in API responses. Search and details return bridge-owned proxy URLs compatible with the XML manifests in `manifests/`.

## Configuration

Secrets are read from files rather than environment values:

| Variable | Default | Purpose |
| --- | --- | --- |
| `ATLAS_BRIDGE_TOKEN_FILE` | `/run/secrets/bridge-token` | Required 32+ character bridge token |
| `ATLAS_ANNA_KEY_FILE` | `/run/secrets/anna-key` | Optional Anna member fast-download key |
| `ATLAS_BRIDGE_PUBLIC_BASE_URL` | `http://atlas-bridge:8080` | Origin written into normalized cover/file URLs |
| `ATLAS_BRIDGE_DATA` | `/data/cache.db` | bbolt metadata cache |
| `ATLAS_ANNA_MIRRORS` | Current `.gd,.gl,.pk` mirrors | Ordered search and API mirrors |
| `ATLAS_ANNA_EXTRA_ORIGINS` | `https://download.booksdl.org` | Exact additional cover/download origins |
| `ATLAS_LIBGEN_MIRRORS` | Current `.gl,.bz,.la,.vg` LibGen+ mirrors | Ordered search mirrors |
| `ATLAS_LIBGEN_EXTRA_ORIGINS` | `https://library.lol,https://download.booksdl.org` | Exact detail/download origins |
| `ATLAS_BRIDGE_DOWNLOAD_LIMIT` | 512 MiB | Maximum proxied response size |
| `ATLAS_BRIDGE_REQUEST_TIMEOUT` | 45 seconds | Complete upstream request timeout |

Redirects are limited and each target must use HTTPS and an explicitly configured origin. DNS is resolved by the bridge and loopback, RFC1918/ULA, link-local, CGNAT, reserved/documentation, multicast, and cloud-metadata addresses are rejected. Add a changed upstream origin explicitly; wildcard origins are intentionally unsupported.

## Development

```sh
go test -race ./...
go vet ./...
go build ./cmd/atlas-bridge
```

For local startup, create secret files and point the service at them:

```sh
ATLAS_BRIDGE_TOKEN_FILE=./secrets/bridge-token \
ATLAS_ANNA_KEY_FILE=./secrets/anna-key \
ATLAS_BRIDGE_DATA=./data/cache.db \
go run ./cmd/atlas-bridge
```

The Anna adapter uses fixed, bounded HTML selectors and never executes page JavaScript. When a mirror returns a DDoS/browser challenge, the API returns `upstream_challenge`; it does not pretend an empty result set was successful. The official member endpoint is used only after an MD5 and requested file have been selected.

The LibGen adapter searches configured mirrors, normalizes classic result tables, and resolves download pages through bounded HTTP hops. Mirror layouts and availability change frequently, so parsing failures are explicit and retryability is communicated in the error response.

## Adding an adapter

Implement the compile-time `model.Provider` interface and register it in `cmd/atlas-bridge/main.go`. An adapter must:

- accept only bounded normalized inputs;
- use `safehttp.Client` with exact origins;
- return stable native identifiers and only EPUB/PDF files;
- keep upstream URLs inside the provider implementation;
- return `model.ProviderError` for expected failure modes;
- include synthetic or public-domain fixtures and security tests.

Runtime plugins, shelling out, JavaScript evaluation, and arbitrary URL forwarding are not accepted.

## Deployment

The deployment files create a rootless Podman network and container without publishing a host port. Kavita Atlas must join `atlas-bridge.network`, and both provider settings must enable private-network access because their manifest base URL is the internal `http://atlas-bridge:8080` service name.

Run `deploy/install.sh` as `kreef` after copying the repository to the server. It generates but does not print the bridge token, installs the provider manifests, and starts the service. Enter that token through Atlas's credential UI. To enable Anna member downloads, create `/home/kreef/.config/atlas-bridge/secrets/anna-key` interactively with mode `0600`, then restart `atlas-bridge.service`.

Metadata cache data contains no credentials or signed download URLs and may be deleted while the service is stopped. The update timer retains the previous image and restores it when the new container fails its health check.
