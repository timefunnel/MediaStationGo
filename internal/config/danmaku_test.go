package config

import "testing"

func TestDanmakuIndependentConfiguration(t *testing.T) {
	for _, raw := range []string{"", "file:///tmp/x", "http://user:pass@localhost", "http://localhost?token=x"} {
		cfg := DanmakuConfig{Enabled: true, URL: raw, Token: "test-token-16-chars"}
		if cfg.validate() == nil {
			t.Fatalf("accepted invalid service URL %q", raw)
		}
	}
	cfg := DanmakuConfig{Enabled: true, URL: " http://127.0.0.1:9322/ ", Token: "test-token-16-chars"}
	if err := cfg.validate(); err != nil || cfg.URL != "http://127.0.0.1:9322" || cfg.TimeoutSeconds != 120 {
		t.Fatalf("config = %+v, error = %v", cfg, err)
	}
	cfg.Token = "short"
	if cfg.validate() == nil {
		t.Fatal("accepted weak token")
	}
}
