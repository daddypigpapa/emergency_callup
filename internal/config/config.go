// Package config reads and validates server configuration from environment
// variables. See docs/SPEC.md §11.1, §12.3.
//
// Rule (SPEC §0-1): fields here must map 1:1 to variables documented in
// docs/SPEC.md §12.3 (.env.example). Do not add config that isn't there.
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Config holds validated server configuration.
type Config struct {
	BaseURL         string // https://mob.example.go.kr (no trailing slash)
	ListenAddr      string // :8080
	DataDir         string // /data
	TileURL         string // may contain literal "{key}" placeholder
	TileKey         string // substituted for {key} before serving to clients
	TileAttribution string

	NaverAppName   string
	NaverRouteType string // car|public|walk|bicycle

	SMSHTTPURL          string // empty => http provider disabled, manual only
	SMSHTTPAuthHeader   string
	SMSHTTPBodyTemplate string
	SMSHTTPRPS          int
	SMSSender           string

	FixRetentionDays int

	TrustProxy bool // true => honor X-Forwarded-For

	TLSCertFile string
	TLSKeyFile  string
}

// SMSHTTPEnabled reports whether the http SMS provider should be registered.
func (c *Config) SMSHTTPEnabled() bool { return c.SMSHTTPURL != "" }

// TLSEnabled reports whether the server should terminate TLS itself.
func (c *Config) TLSEnabled() bool { return c.TLSCertFile != "" && c.TLSKeyFile != "" }

var validRouteTypes = map[string]bool{"car": true, "public": true, "walk": true, "bicycle": true}

// Load reads configuration via getenv (normally os.Getenv) and validates it.
// On any validation failure it returns a non-nil error describing every
// problem found (not just the first) so an operator can fix them all at
// once before restarting.
func Load(getenv func(string) string) (*Config, error) {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }

	c := &Config{
		BaseURL:             strings.TrimRight(get("BASE_URL"), "/"),
		ListenAddr:          get("LISTEN_ADDR"),
		DataDir:             get("DATA_DIR"),
		TileURL:             get("TILE_URL"),
		TileKey:             get("TILE_KEY"),
		TileAttribution:     get("TILE_ATTRIBUTION"),
		NaverAppName:        get("NAVER_APPNAME"),
		NaverRouteType:      get("NAVER_ROUTE_TYPE"),
		SMSHTTPURL:          get("SMS_HTTP_URL"),
		SMSHTTPAuthHeader:   get("SMS_HTTP_AUTH_HEADER"),
		SMSHTTPBodyTemplate: get("SMS_HTTP_BODY_TEMPLATE"),
		SMSSender:           get("SMS_SENDER"),
		TLSCertFile:         get("TLS_CERT_FILE"),
		TLSKeyFile:          get("TLS_KEY_FILE"),
	}

	if c.ListenAddr == "" {
		c.ListenAddr = ":8080"
	}
	if c.NaverRouteType == "" {
		c.NaverRouteType = "car"
	}

	var errs []string

	if c.BaseURL == "" {
		errs = append(errs, "BASE_URL is required")
	} else if !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://") {
		errs = append(errs, "BASE_URL must start with http:// or https://")
	}
	if c.DataDir == "" {
		errs = append(errs, "DATA_DIR is required")
	}
	if c.TileURL == "" {
		errs = append(errs, "TILE_URL is required")
	} else if strings.Contains(c.TileURL, "{key}") && c.TileKey == "" {
		errs = append(errs, "TILE_KEY is required because TILE_URL contains {key}")
	}
	if c.NaverAppName == "" {
		errs = append(errs, "NAVER_APPNAME is required")
	}
	if !validRouteTypes[c.NaverRouteType] {
		errs = append(errs, "NAVER_ROUTE_TYPE must be one of car|public|walk|bicycle")
	}

	if c.SMSHTTPURL != "" {
		if c.SMSHTTPAuthHeader == "" {
			errs = append(errs, "SMS_HTTP_AUTH_HEADER is required because SMS_HTTP_URL is set")
		}
		if c.SMSHTTPBodyTemplate == "" {
			errs = append(errs, "SMS_HTTP_BODY_TEMPLATE is required because SMS_HTTP_URL is set")
		}
		if c.SMSSender == "" {
			errs = append(errs, "SMS_SENDER is required because SMS_HTTP_URL is set")
		}
	}

	rpsStr := get("SMS_HTTP_RPS")
	if rpsStr == "" {
		c.SMSHTTPRPS = 10
	} else if v, err := strconv.Atoi(rpsStr); err != nil || v <= 0 {
		errs = append(errs, "SMS_HTTP_RPS must be a positive integer")
	} else {
		c.SMSHTTPRPS = v
	}

	retStr := get("FIX_RETENTION_DAYS")
	if retStr == "" {
		c.FixRetentionDays = 30
	} else if v, err := strconv.Atoi(retStr); err != nil || v <= 0 {
		errs = append(errs, "FIX_RETENTION_DAYS must be a positive integer")
	} else {
		c.FixRetentionDays = v
	}

	tpStr := get("TRUST_PROXY")
	if tpStr == "" {
		c.TrustProxy = false
	} else if v, err := strconv.ParseBool(tpStr); err != nil {
		errs = append(errs, "TRUST_PROXY must be true or false")
	} else {
		c.TrustProxy = v
	}

	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		errs = append(errs, "TLS_CERT_FILE and TLS_KEY_FILE must both be set or both be empty")
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("configuration invalid:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return c, nil
}

// Summary returns a one-line, secret-free summary for startup logs
// (SPEC §12.4: "기동 로그에 설정 요약(비밀값 제외)").
func (c *Config) Summary() string {
	sms := "manual only"
	if c.SMSHTTPEnabled() {
		sms = "manual + http"
	}
	tls := "off (reverse proxy expected)"
	if c.TLSEnabled() {
		tls = "on (built-in)"
	}
	return fmt.Sprintf(
		"base_url=%s listen=%s data_dir=%s tile=%s sms=%s tls=%s fix_retention_days=%d trust_proxy=%v",
		c.BaseURL, c.ListenAddr, c.DataDir, maskTileURL(c.TileURL), sms, tls, c.FixRetentionDays, c.TrustProxy,
	)
}

func maskTileURL(u string) string {
	if strings.Contains(u, "{key}") {
		return u
	}
	return u
}
