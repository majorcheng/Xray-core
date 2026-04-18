package command

import (
	"testing"

	routerapp "github.com/xtls/xray-core/app/router"
	cserial "github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
)

func TestExtractRoutingConfigFromCoreUsesRouterApp(t *testing.T) {
	want := &routerapp.Config{
		DomainStrategy: routerapp.Config_IpOnDemand,
	}

	config, err := extractRoutingConfigFromCore(&core.Config{
		App: []*cserial.TypedMessage{
			cserial.ToTypedMessage(want),
		},
	})
	if err != nil {
		t.Fatalf("extract routing config failed: %v", err)
	}
	if config.GetDomainStrategy() != want.GetDomainStrategy() {
		t.Fatalf("unexpected domain strategy: got=%v want=%v", config.GetDomainStrategy(), want.GetDomainStrategy())
	}
}

func TestExtractRoutingConfigFromCoreFallsBackToEmptyConfig(t *testing.T) {
	config, err := extractRoutingConfigFromCore(&core.Config{})
	if err != nil {
		t.Fatalf("extract empty routing config failed: %v", err)
	}
	if config == nil {
		t.Fatal("expected empty routing config, got nil")
	}
	if len(config.Rule) != 0 {
		t.Fatalf("expected no routing rules, got %d", len(config.Rule))
	}
}
