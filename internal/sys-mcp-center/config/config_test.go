package config

import "testing"

func TestValidate_RequireInternalAddressWhenDatabaseAndHAEnabled(t *testing.T) {
	cfg := &CenterConfig{
		Listen: Listen{
			HTTPAddress: ":8080",
			GRPCAddress: ":9090",
		},
		Auth: Auth{
			AgentTokens: []string{"tok"},
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
			AgentTokens: []string{"tok"},
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
