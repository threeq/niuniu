package config

import "testing"

func TestEnforceLoopbackWhenUnauthenticated(t *testing.T) {
	t.Setenv("NIUNIU_ALLOW_INSECURE_BIND", "")

	newCfg := func(authEnabled bool, host string) *Config {
		c := &Config{}
		c.Auth.Enabled = authEnabled
		c.Server.Host = host
		return c
	}

	cases := []struct {
		name          string
		authEnabled   bool
		host          string
		wantHost      string
		wantOverride  bool
	}{
		{"unauth + 0.0.0.0 -> forced loopback", false, "0.0.0.0", "127.0.0.1", true},
		{"unauth + :: -> forced loopback", false, "::", "127.0.0.1", true},
		{"unauth + public IP -> forced loopback", false, "192.168.1.10", "127.0.0.1", true},
		{"unauth + already loopback -> untouched", false, "127.0.0.1", "127.0.0.1", false},
		{"unauth + localhost -> untouched", false, "localhost", "localhost", false},
		{"auth enabled + 0.0.0.0 -> untouched", true, "0.0.0.0", "0.0.0.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newCfg(tc.authEnabled, tc.host)
			_, overridden := EnforceLoopbackWhenUnauthenticated(cfg)
			if overridden != tc.wantOverride {
				t.Fatalf("overridden = %v, want %v", overridden, tc.wantOverride)
			}
			if cfg.Server.Host != tc.wantHost {
				t.Fatalf("host = %q, want %q", cfg.Server.Host, tc.wantHost)
			}
		})
	}
}

func TestEnforceLoopbackOptOut(t *testing.T) {
	t.Setenv("NIUNIU_ALLOW_INSECURE_BIND", "1")
	cfg := &Config{}
	cfg.Auth.Enabled = false
	cfg.Server.Host = "0.0.0.0"
	if _, overridden := EnforceLoopbackWhenUnauthenticated(cfg); overridden {
		t.Fatal("opt-out env set: expected no override")
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Fatalf("host = %q, want 0.0.0.0 (unchanged)", cfg.Server.Host)
	}
}
