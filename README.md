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
| `ATLAS_BRIDGE_CATALOGUE_JSONL` | empty | Optional normalized JSONL catalogue to ingest at startup |
| `ATLAS_BRIDGE_CATALOGUE_ZSTD` | empty | Optional `.jsonl.zst`/seekable-zstd catalogue to stream-ingest at startup |
| `ATLAS_BRIDGE_CATALOGUE_TORRENT` | empty | Optional administrator-configured torrent URL/magnet for the metadata payload |
| `ATLAS_BRIDGE_CATALOGUE_TORRENT_PATH` | empty | Relative path of the selected `.jsonl.zst` file inside that torrent |
| `ATLAS_BRIDGE_CATALOGUE_FILES_TORRENT` | empty | Optional zlib3_files metadata torrent descriptor/magnet |
| `ATLAS_BRIDGE_CATALOGUE_FILES_TORRENT_PATH` | empty | Relative `.jsonl.zst` path inside the zlib3_files torrent |
| `ATLAS_BRIDGE_CATALOGUE_FILES_ZSTD` | empty | Destination path for the file-level metadata payload |
| `ATLAS_BRIDGE_CATALOGUE_PAYLOAD_LIMIT` | 4 GiB | Maximum compressed metadata payload staged from Transmission |
| `ATLAS_BRIDGE_CATALOGUE_MAX_EXPANDED` | 50 GiB | Hard expanded-size ceiling for compressed metadata ingestion |
| `ATLAS_ANNA_MIRRORS` | Current `.gd,.gl,.pk` mirrors | Ordered search and API mirrors |
| `ATLAS_ANNA_EXTRA_ORIGINS` | `https://download.booksdl.org` | Exact additional cover/download origins |
| `ATLAS_LIBGEN_MIRRORS` | Current `.gl,.bz,.la,.vg` LibGen+ mirrors | Ordered search mirrors |
| `ATLAS_LIBGEN_EXTRA_ORIGINS` | Known Library Genesis download/CDN origins | Exact detail/download origins |
| `ATLAS_METADATA_ORIGINS` | Open Library search and cover origins | Exact metadata-enrichment origins |
| `ATLAS_BRIDGE_DOWNLOAD_LIMIT` | 512 MiB | Maximum proxied response size |
| `ATLAS_BRIDGE_REQUEST_TIMEOUT` | 45 seconds | Complete upstream request timeout |

Redirects are limited and each target must use HTTPS and an explicitly configured origin. DNS is resolved by the bridge and loopback, RFC1918/ULA, link-local, CGNAT, reserved/documentation, multicast, and cloud-metadata addresses are rejected. Add a changed upstream origin explicitly. The only host-family exception is the compile-time `.us.archive.org` CDN suffix used by Open Library covers; it is not configurable by manifests or requests and retains the same DNS and redirect checks.

## Development

```sh
go test -race ./...
go vet ./...
go build ./cmd/atlas-bridge
```

To inspect a torrent descriptor without downloading any payload, build and run:

```sh
go run ./cmd/catalogue-inspect ./metadata.torrent
```

The command prints the infohash, total size, and every contained path. It is
the required reconnaissance step before configuring a metadata torrent; a
`.torrent` descriptor is never treated as a searchable catalogue by itself.

For local startup, create secret files and point the service at them:

```sh
ATLAS_BRIDGE_TOKEN_FILE=./secrets/bridge-token \
ATLAS_ANNA_KEY_FILE=./secrets/anna-key \
ATLAS_BRIDGE_DATA=./data/cache.db \
go run ./cmd/atlas-bridge
```

The Anna adapter uses fixed, bounded HTML selectors and never executes page JavaScript. When a mirror returns a DDoS/browser challenge, the API returns `upstream_challenge`; it does not pretend an empty result set was successful. The official member endpoint is used only after an MD5 and requested file have been selected.

For local catalogue mode, either point `ATLAS_BRIDGE_CATALOGUE_ZSTD` at a mounted lawful/public-domain metadata fixture, or configure `ATLAS_BRIDGE_CATALOGUE_TORRENT` plus `ATLAS_BRIDGE_CATALOGUE_TORRENT_PATH`. On startup the bridge retrieves only that configured metadata file into a private staging directory, atomically renames it, then streams JSONL records through a single zstd decoder into SQLite/FTS5. It does not download or index content payload torrents. `zlib3_records` supplies searchable metadata and `zlib3_files` joins the corresponding MD5, AACID, and data-torrent folder by the stable source ID. Search only advertises formats with a deterministic direct or torrent locator.

For an acquisition, Bridge first uses the configured member resolver. If it is unavailable, Bridge derives the validated official data-torrent descriptor and exact AACID path, adds the torrent paused, disables every unrelated file, and starts only the requested entry through private Transmission. Book payloads remain in staging and never enter a Kavita library directly. Covers are enriched through the bounded Open Library search and cover origins and successful mappings are cached in the catalogue database.

The LibGen adapter searches configured mirrors, normalizes classic result tables, and resolves download pages through bounded HTTP hops. Mirror layouts and availability change frequently, so parsing failures are explicit and retryability is communicated in the error response.

LibGen also rotates the CDN hosts it redirects downloads to. Each one must be listed in `ATLAS_LIBGEN_EXTRA_ORIGINS`, which production sets in `deploy/atlas-bridge.container`. To check live downloads with exactly that configuration, run:

```sh
go test -tags live -run TestLiveLibGenDownloads -count=1 -v ./internal/providers/
```

The test searches LibGen for `ATLAS_LIVE_QUERY` (default "Mechanized Farming") and opens the first `ATLAS_LIVE_BOOKS` files (default 5). For each one it prints the real error, such as `origin cdn4.booksdl.lc is not allowed`. It reads only each file's signature and saves nothing.

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

`deploy/` is the source of truth for the production library host. That host is M900 (`lab-pta-03`), where the locked rootless `lab-library` account runs Kavita Atlas, Atlas Bridge and Transmission from its secondary SSD. [deploy/library.env](deploy/library.env) holds the layout. Paths under `LIBRARY_ROOT` (the `current` generation symlink) are shared with the homelab backup and maintenance tools, so keep them stable.

The Quadlets create a rootless Podman network and containers without publishing a host port. Kavita Atlas joins `atlas-bridge.network`. Both provider settings must enable private-network access, because their manifest base URL is the internal service name `http://atlas-bridge:8080`.

Run the installer as root on the library host from a checkout of this repository:

```sh
sudo bash deploy/install.sh            # compare repo with live files; changes nothing
sudo bash deploy/install.sh --apply    # back up, install, verify, roll back on failure
sudo bash <backup-dir>/restore.sh      # return to a backup
```

`--apply` waits for the shared update/backup lock and refuses to run if production is unhealthy or an image update is mid-recovery. It then writes a backup to `$LIBRARY_HOME/deploy-backups/<UTC time>/`. The backup holds the managed files, their hashes, unit enablement and running image IDs, a tarball of both unit directories, and a standalone `restore.sh`. After installing, it validates the Quadlets with the Podman generator, reloads systemd, and restarts the bridge or Transmission only when their definitions changed in substance. It then checks that the bridge, Transmission and Kavita are all healthy. Any failure restores the backup automatically.

The installer manages only Atlas Bridge's own files and never touches Kavita's definitions, data, secrets or images. It keeps the live `Image=` lines, so it never changes which image runs. Provider manifests are compared but never overwritten. On a fresh host it also creates the data directories, the lock and the registry auth file. It generates a bridge token without printing it (enter that token in Atlas's credential UI) and installs the manifests. To enable Anna member downloads, create `$LIBRARY_ROOT/.config/atlas-bridge/secrets/anna-key` interactively with mode `0600`, then restart `atlas-bridge.service`.

Images are updated by `atlas-bridge-update.timer`, which runs [deploy/atlas-bridge-updater.py](deploy/atlas-bridge-updater.py) every 30 minutes. The updater is installed root-owned at `LIBRARY_UPDATER`. It pulls `ghcr.io/elhefe3/atlas-bridge:edge` and retags it into the Quadlet's local pin. A durable recovery record restores the previous image if the new container fails its health check, including after an interrupted update. Catalogue ingestion is controlled by `/data/catalogue-ingest.pause` and the `atlas-bridge-ingest-*` timers, which are installed disabled.

Metadata cache data contains no credentials or signed download URLs and may be deleted while the service is stopped.
