package config

import "testing"

func TestValidate_RequireInternalAddressWhenDatabaseAndHAEnabled(t *testing.T) {
	cfg := &CenterConfig{
		Listen: Listen{
			HTTPAddress: ":18880",
			GRPCAddress: ":18890",
		},
		Auth: Auth{
			ClientTokens: []string{"client"},
			AgentTokens:  []string{"agent"},
			ProxyTokens:  []string{"proxy"},
		},
		Database: Database{
			Enable: true,
			DSN:    "postgres://example",
		},
		HA: HA{
			InternalUseTLS: true,
		},
	}

	if err := validate(cfg); err == nil {
		t.Fatal("expected validate to require ha.internal_address when database is enabled")
	}
}

func TestValidate_AcceptInternalAddressWhenDatabaseEnabled(t *testing.T) {
	cfg := &CenterConfig{
		Auth: Auth{
			ClientTokens: []string{"client"},
			AgentTokens:  []string{"agent"},
			ProxyTokens:  []string{"proxy"},
		},
		Database: Database{
			Enable: true,
			DSN:    "postgres://example",
		},
		HA: HA{
			InternalAddress: "center-a.internal:8443",
			InternalUseTLS:  true,
		},
	}

	if err := validate(cfg); err != nil {
		t.Fatalf("expected validate to accept explicit ha.internal_address, got %v", err)
	}
}

func TestValidate_RejectDuplicateTokensAcrossDomains(t *testing.T) {
	cfg := &CenterConfig{
		Auth: Auth{
			ClientTokens: []string{"dup"},
			AgentTokens:  []string{"dup"},
			ProxyTokens:  []string{"proxy"},
		},
	}

	if err := validate(cfg); err == nil {
		t.Fatal("expected duplicate token validation error")
	}
}
