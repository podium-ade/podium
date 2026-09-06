// Package artifacts is the control plane's half of the object store: the S3 client, the
// key layout, presigned URLs, and the service that records what has been stored.
//
// Nodes never talk to S3. A node hands a file to NodeService.UploadArtifact and the server
// is what writes it — "nodes only ever talk to the server" is the invariant the whole
// networking design rests on, and a presigned PUT straight from a node would break it.
package artifacts

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

// MaxArtifactBytes is the largest single artifact Podium will store: 512 MB.
const MaxArtifactBytes int64 = 512 << 20

// Config is the PODIUM_S3_* environment. An empty Endpoint means artifacts are disabled:
// tasks still run, they just cannot produce files and their logs are never rolled up.
type Config struct {
	// Endpoint is PODIUM_S3_ENDPOINT: host:port, or a full URL whose scheme picks TLS.
	Endpoint string
	// Bucket is PODIUM_S3_BUCKET. It is created on start if it does not exist.
	Bucket string
	// AccessKey is PODIUM_S3_ACCESS_KEY.
	AccessKey string
	// SecretKey is PODIUM_S3_SECRET_KEY. SENSITIVE: never log it.
	SecretKey string
	// Region is PODIUM_S3_REGION, default us-east-1. The bundled object store ignores it; a
	// real S3 does not.
	Region string
	// UseSSL is derived from the endpoint's scheme, or PODIUM_S3_USE_SSL when the endpoint
	// carries none.
	UseSSL bool
}

// DefaultRegion is what an S3 endpoint that does not care is told.
const DefaultRegion = "us-east-1"

// ConfigFromEnv reads the canonical PODIUM_S3_* variables.
func ConfigFromEnv() Config {
	endpoint, ssl := splitEndpoint(os.Getenv("PODIUM_S3_ENDPOINT"))
	if v := os.Getenv("PODIUM_S3_USE_SSL"); v != "" && !strings.Contains(os.Getenv("PODIUM_S3_ENDPOINT"), "://") {
		ssl = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	region := os.Getenv("PODIUM_S3_REGION")
	if region == "" {
		region = DefaultRegion
	}
	return Config{
		Endpoint:  endpoint,
		Bucket:    os.Getenv("PODIUM_S3_BUCKET"),
		AccessKey: os.Getenv("PODIUM_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("PODIUM_S3_SECRET_KEY"),
		Region:    region,
		UseSSL:    ssl,
	}
}

// splitEndpoint accepts both "objectstore:9000" and "https://objectstore:9000" and reports the host
// form minio-go wants plus whether TLS was asked for.
func splitEndpoint(raw string) (host string, ssl bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if !strings.Contains(raw, "://") {
		return strings.TrimSuffix(raw, "/"), false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw, false
	}
	return u.Host, u.Scheme == "https"
}

// Enabled reports whether an object store is configured at all.
func (c Config) Enabled() bool { return c.Endpoint != "" }

// Validate reports the first thing that would stop the client from being built. A
// disabled config is valid.
func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	if c.Bucket == "" {
		return errors.New("PODIUM_S3_BUCKET is required when PODIUM_S3_ENDPOINT is set")
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return errors.New("PODIUM_S3_ACCESS_KEY and PODIUM_S3_SECRET_KEY are required when PODIUM_S3_ENDPOINT is set")
	}
	return nil
}
