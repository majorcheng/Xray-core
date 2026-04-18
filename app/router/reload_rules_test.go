package router

import "testing"

func TestReloadRulesReplaceUpdatesDomainStrategy(t *testing.T) {
	r := &Router{
		domainStrategy: Config_IpIfNonMatch,
		balancers:      map[string]*Balancer{},
		rules:          []*Rule{},
	}

	if err := r.ReloadRules(&Config{DomainStrategy: Config_IpOnDemand}, false); err != nil {
		t.Fatalf("reload rules failed: %v", err)
	}
	if r.domainStrategy != Config_IpOnDemand {
		t.Fatalf("unexpected domain strategy after replace reload: got=%v want=%v", r.domainStrategy, Config_IpOnDemand)
	}
}

func TestReloadRulesAppendKeepsCurrentDomainStrategy(t *testing.T) {
	r := &Router{
		domainStrategy: Config_IpIfNonMatch,
		balancers:      map[string]*Balancer{},
		rules:          []*Rule{},
	}

	if err := r.ReloadRules(&Config{DomainStrategy: Config_IpOnDemand}, true); err != nil {
		t.Fatalf("reload rules failed: %v", err)
	}
	if r.domainStrategy != Config_IpIfNonMatch {
		t.Fatalf("unexpected domain strategy after append reload: got=%v want=%v", r.domainStrategy, Config_IpIfNonMatch)
	}
}
