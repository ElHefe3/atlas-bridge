package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddress         string
	PublicBaseURL         string
	DataPath              string
	BridgeToken           string
	AnnaKey               string
	AnnaMirrors           []string
	AnnaOrigins           []string
	LibGenMirrors         []string
	LibGenOrigins         []string
	MetadataLimit         int64
	DownloadLimit         int64
	RequestTimeout        time.Duration
	CatalogueJSONL        string
	CatalogueZstd         string
	CatalogueTorrent      string
	CatalogueTorrentPath  string
	CatalogueMaxExpanded  int64
	CataloguePayloadLimit int64
	TransmissionRPC       string
}

func Load() (Config, error) {
	c := Config{
		ListenAddress:         env("ATLAS_BRIDGE_LISTEN", ":8080"),
		PublicBaseURL:         strings.TrimRight(env("ATLAS_BRIDGE_PUBLIC_BASE_URL", "http://atlas-bridge:8080"), "/"),
		DataPath:              env("ATLAS_BRIDGE_DATA", "/data/cache.db"),
		AnnaMirrors:           list("ATLAS_ANNA_MIRRORS", "https://annas-archive.gd,https://annas-archive.gl,https://annas-archive.pk"),
		LibGenMirrors:         list("ATLAS_LIBGEN_MIRRORS", "https://libgen.gl,https://libgen.bz,https://libgen.la,https://libgen.vg"),
		MetadataLimit:         intEnv("ATLAS_BRIDGE_METADATA_LIMIT", 10<<20),
		DownloadLimit:         intEnv("ATLAS_BRIDGE_DOWNLOAD_LIMIT", 512<<20),
		RequestTimeout:        durationEnv("ATLAS_BRIDGE_REQUEST_TIMEOUT", 45*time.Second),
		CatalogueJSONL:        env("ATLAS_BRIDGE_CATALOGUE_JSONL", ""),
		CatalogueZstd:         env("ATLAS_BRIDGE_CATALOGUE_ZSTD", ""),
		CatalogueTorrent:      env("ATLAS_BRIDGE_CATALOGUE_TORRENT", ""),
		CatalogueTorrentPath:  env("ATLAS_BRIDGE_CATALOGUE_TORRENT_PATH", ""),
		CatalogueMaxExpanded:  intEnv("ATLAS_BRIDGE_CATALOGUE_MAX_EXPANDED", 50<<30),
		CataloguePayloadLimit: intEnv("ATLAS_BRIDGE_CATALOGUE_PAYLOAD_LIMIT", 4<<30),
		TransmissionRPC:       env("ATLAS_TRANSMISSION_RPC", "http://transmission:9091/transmission/rpc"),
	}
	c.AnnaOrigins = merge(c.AnnaMirrors, list("ATLAS_ANNA_EXTRA_ORIGINS", "https://download.booksdl.org"))
	c.LibGenOrigins = merge(c.LibGenMirrors, list("ATLAS_LIBGEN_EXTRA_ORIGINS", "https://library.lol,https://download.booksdl.org,https://cdn1.booksdl.lc,https://cdn2.booksdl.lc,https://cdn3.booksdl.lc"))
	var err error
	c.BridgeToken, err = secret("ATLAS_BRIDGE_TOKEN_FILE", "/run/secrets/bridge-token", true)
	if err != nil {
		return Config{}, err
	}
	c.AnnaKey, err = secret("ATLAS_ANNA_KEY_FILE", "/run/secrets/anna-key", false)
	if err != nil {
		return Config{}, err
	}
	if len(c.BridgeToken) < 32 {
		return Config{}, errors.New("bridge token must contain at least 32 characters")
	}
	if c.PublicBaseURL == "" {
		return Config{}, errors.New("public base URL is required")
	}
	return c, nil
}

func env(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
func list(name, fallback string) []string {
	raw := env(name, fallback)
	var values []string
	for _, v := range strings.Split(raw, ",") {
		if x := strings.TrimSpace(v); x != "" {
			values = append(values, strings.TrimRight(x, "/"))
		}
	}
	return values
}
func merge(groups ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, group := range groups {
		for _, v := range group {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}
func intEnv(name string, fallback int64) int64 {
	value, err := strconv.ParseInt(env(name, fmt.Sprint(fallback)), 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
func durationEnv(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(env(name, fallback.String()))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
func secret(envName, fallback string, required bool) (string, error) {
	path := env(envName, fallback)
	data, err := os.ReadFile(path)
	if err != nil {
		if !required && os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", envName, err)
	}
	value := strings.TrimSpace(string(data))
	if required && value == "" {
		return "", fmt.Errorf("%s is empty", envName)
	}
	return value, nil
}
