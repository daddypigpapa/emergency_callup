package config

import "testing"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validBase() map[string]string {
	return map[string]string{
		"BASE_URL":      "https://mob.example.go.kr",
		"DATA_DIR":      "/data",
		"TILE_URL":      "https://api.vworld.kr/req/wmts/1.0.0/{key}/Base/{z}/{y}/{x}.png",
		"TILE_KEY":      "abc123",
		"NAVER_APPNAME": "mob.example.go.kr",
	}
}

// R6-adjacent: missing required config must fail startup (exit path is in cmd/server).
func TestLoad_MissingRequired(t *testing.T) {
	_, err := Load(env(map[string]string{}))
	if err == nil {
		t.Fatal("expected error for empty config")
	}
}

func TestLoad_ValidMinimal(t *testing.T) {
	c, err := Load(env(validBase()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ListenAddr != ":8080" {
		t.Errorf("default ListenAddr = %q, want :8080", c.ListenAddr)
	}
	if c.NaverRouteType != "car" {
		t.Errorf("default NaverRouteType = %q, want car", c.NaverRouteType)
	}
	if c.SMSHTTPRPS != 10 {
		t.Errorf("default SMSHTTPRPS = %d, want 10", c.SMSHTTPRPS)
	}
	if c.FixRetentionDays != 30 {
		t.Errorf("default FixRetentionDays = %d, want 30", c.FixRetentionDays)
	}
	if c.SMSHTTPEnabled() {
		t.Error("SMSHTTPEnabled should be false when SMS_HTTP_URL unset")
	}
}

func TestLoad_TileKeyRequiredWhenPlaceholderPresent(t *testing.T) {
	m := validBase()
	delete(m, "TILE_KEY")
	_, err := Load(env(m))
	if err == nil {
		t.Fatal("expected error when TILE_URL has {key} but TILE_KEY is empty")
	}
}

func TestLoad_TileKeyNotRequiredWithoutPlaceholder(t *testing.T) {
	m := validBase()
	delete(m, "TILE_KEY")
	m["TILE_URL"] = "https://tiles.example.org/{z}/{x}/{y}.png"
	_, err := Load(env(m))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_SMSHTTPRequiresCompanionVars(t *testing.T) {
	m := validBase()
	m["SMS_HTTP_URL"] = "https://sms.example.com/send"
	_, err := Load(env(m))
	if err == nil {
		t.Fatal("expected error: SMS_HTTP_URL set without auth header/template/sender")
	}

	m["SMS_HTTP_AUTH_HEADER"] = "Authorization: Bearer x"
	m["SMS_HTTP_BODY_TEMPLATE"] = "{{.Mobile}}:{{.Text}}"
	m["SMS_SENDER"] = "0212345678"
	c, err := Load(env(m))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !c.SMSHTTPEnabled() {
		t.Error("SMSHTTPEnabled should be true once all companion vars are set")
	}
}

func TestLoad_InvalidRouteType(t *testing.T) {
	m := validBase()
	m["NAVER_ROUTE_TYPE"] = "bike"
	_, err := Load(env(m))
	if err == nil {
		t.Fatal("expected error for invalid NAVER_ROUTE_TYPE")
	}
}

func TestLoad_TLSBothOrNeither(t *testing.T) {
	m := validBase()
	m["TLS_CERT_FILE"] = "/data/cert.pem"
	_, err := Load(env(m))
	if err == nil {
		t.Fatal("expected error when only TLS_CERT_FILE is set")
	}
}

func TestLoad_BadBaseURLScheme(t *testing.T) {
	m := validBase()
	m["BASE_URL"] = "mob.example.go.kr"
	_, err := Load(env(m))
	if err == nil {
		t.Fatal("expected error for BASE_URL without scheme")
	}
}
