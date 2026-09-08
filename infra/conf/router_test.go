package conf_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
	_ "unsafe"

	"github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/common/geodata"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	. "github.com/xtls/xray-core/infra/conf"

	"google.golang.org/protobuf/proto"
)

func TestRouterConfigChampionQualityMode(t *testing.T) {
	for _, mode := range []string{"off", "shadow", "select", " SELECT ", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			var config RouterConfig
			input := fmt.Sprintf(`{"balancers":[{"tag":"quality","selector":["proxy-"],"strategy":{"type":"champion","settings":{"qualityMode":%q}}}]}`, mode)
			if err := json.Unmarshal([]byte(input), &config); err != nil {
				t.Fatal(err)
			}
			built, err := config.Build()
			if mode == "invalid" {
				if err == nil {
					t.Fatal("invalid qualityMode accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			settings := built.BalancingRule[0].StrategySettings
			if mode == "off" {
				if settings != nil {
					t.Fatal("explicit off changed default config")
				}
				return
			}
			value, err := settings.GetInstance()
			if err != nil {
				t.Fatal(err)
			}
			want := mode
			if mode == " SELECT " {
				want = "select"
			}
			if got := value.(*router.StrategyChampionConfig).QualityMode; got != want {
				t.Fatalf("mode=%q want %q", got, want)
			}
		})
	}
}

func TestRouterConfig(t *testing.T) {
	createParser := func() func(string) (proto.Message, error) {
		return func(s string) (proto.Message, error) {
			config := new(RouterConfig)
			if err := json.Unmarshal([]byte(s), config); err != nil {
				return nil, err
			}
			return config.Build()
		}
	}

	runMultiTestCase(t, []TestCase{
		{
			Input: `{
				"domainStrategy": "AsIs",
				"rules": [
					{
						"domain": [
							"baidu.com",
							"qq.com"
						],
						"outboundTag": "direct"
					},
					{
						"ip": [
							"10.0.0.0/8",
							"::1/128"
						],
						"outboundTag": "test"
					},{
						"port": "53, 443, 1000-2000",
						"outboundTag": "test"
					},{
						"port": 123,
						"outboundTag": "test"
					}
				],
				"balancers": [
					{
						"tag": "b1",
						"selector": ["test"],
						"fallbackTag": "fall"
					},
					{
						"tag": "b2",
						"selector": ["test"],
						"strategy": {
							"type": "leastload",
							"settings": {
								"healthCheck": {
									"interval": "5m0s",
									"sampling": 2,
									"timeout": "5s",
									"destination": "dest",
									"connectivity": "conn"
								},
								"costs": [
									{
										"regexp": true,
										"match": "\\d+(\\.\\d+)",
										"value": 5
									}
								],
								"baselines": ["400ms", "600ms"],
								"expected": 6,
								"maxRTT": "1000ms",
								"tolerance": 0.5
							}
						},
						"fallbackTag": "fall"
					}
				]
			}`,
			Parser: createParser(),
			Output: &router.Config{
				DomainStrategy: router.Config_AsIs,
				BalancingRule: []*router.BalancingRule{
					{
						Tag:              "b1",
						OutboundSelector: []string{"test"},
						Strategy:         "random",
						FallbackTag:      "fall",
					},
					{
						Tag:              "b2",
						OutboundSelector: []string{"test"},
						Strategy:         "leastload",
						StrategySettings: serial.ToTypedMessage(&router.StrategyLeastLoadConfig{
							Costs: []*router.StrategyWeight{
								{
									Regexp: true,
									Match:  "\\d+(\\.\\d+)",
									Value:  5,
								},
							},
							Baselines: []int64{
								int64(time.Duration(400) * time.Millisecond),
								int64(time.Duration(600) * time.Millisecond),
							},
							Expected:  6,
							MaxRTT:    int64(time.Duration(1000) * time.Millisecond),
							Tolerance: 0.5,
						}),
						FallbackTag: "fall",
					},
				},
				Rule: []*router.RoutingRule{
					{
						Domain: []*geodata.DomainRule{
							{Value: &geodata.DomainRule_Custom{Custom: &geodata.Domain{Type: geodata.Domain_Substr, Value: "baidu.com"}}},
							{Value: &geodata.DomainRule_Custom{Custom: &geodata.Domain{Type: geodata.Domain_Substr, Value: "qq.com"}}},
						},
						TargetTag: &router.RoutingRule_Tag{
							Tag: "direct",
						},
					},
					{
						Ip: []*geodata.IPRule{
							{
								Value: &geodata.IPRule_Custom{
									Custom: &geodata.CIDRRule{
										Cidr: &geodata.CIDR{Ip: []byte{10, 0, 0, 0}, Prefix: 8},
									},
								},
							},
							{
								Value: &geodata.IPRule_Custom{
									Custom: &geodata.CIDRRule{
										Cidr: &geodata.CIDR{Ip: []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, Prefix: 128},
									},
								},
							},
						},
						TargetTag: &router.RoutingRule_Tag{
							Tag: "test",
						},
					},
					{
						PortList: &net.PortList{
							Range: []*net.PortRange{
								{From: 53, To: 53},
								{From: 443, To: 443},
								{From: 1000, To: 2000},
							},
						},
						TargetTag: &router.RoutingRule_Tag{
							Tag: "test",
						},
					},
					{
						PortList: &net.PortList{
							Range: []*net.PortRange{
								{From: 123, To: 123},
							},
						},
						TargetTag: &router.RoutingRule_Tag{
							Tag: "test",
						},
					},
				},
			},
		},
		{
			Input: `{
				"domainStrategy": "IPIfNonMatch",
				"rules": [
					{
						"domain": [
							"baidu.com",
							"qq.com"
						],
						"outboundTag": "direct"
					},
					{
						"ip": [
							"10.0.0.0/8",
							"::1/128"
						],
						"outboundTag": "test"
					}
				]
			}`,
			Parser: createParser(),
			Output: &router.Config{
				DomainStrategy: router.Config_IpIfNonMatch,
				Rule: []*router.RoutingRule{
					{
						Domain: []*geodata.DomainRule{
							{Value: &geodata.DomainRule_Custom{Custom: &geodata.Domain{Type: geodata.Domain_Substr, Value: "baidu.com"}}},
							{Value: &geodata.DomainRule_Custom{Custom: &geodata.Domain{Type: geodata.Domain_Substr, Value: "qq.com"}}},
						},
						TargetTag: &router.RoutingRule_Tag{
							Tag: "direct",
						},
					},
					{
						Ip: []*geodata.IPRule{
							{
								Value: &geodata.IPRule_Custom{
									Custom: &geodata.CIDRRule{
										Cidr: &geodata.CIDR{Ip: []byte{10, 0, 0, 0}, Prefix: 8},
									},
								},
							},
							{
								Value: &geodata.IPRule_Custom{
									Custom: &geodata.CIDRRule{
										Cidr: &geodata.CIDR{Ip: []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, Prefix: 128},
									},
								},
							},
						},
						TargetTag: &router.RoutingRule_Tag{
							Tag: "test",
						},
					},
				},
			},
		},
	})
}

func TestRouterConfigChampionStrategy(t *testing.T) {
	createParser := func() func(string) (proto.Message, error) {
		return func(s string) (proto.Message, error) {
			config := new(RouterConfig)
			if err := json.Unmarshal([]byte(s), config); err != nil {
				return nil, err
			}
			return config.Build()
		}
	}

	runMultiTestCase(t, []TestCase{
		{
			Input: `{
				"balancers": [
					{
						"tag": "b1",
						"selector": ["test"],
						"strategy": {
							"type": "champion"
						},
						"fallbackTag": "fall"
					}
				]
			}`,
			Parser: createParser(),
			Output: &router.Config{
				DomainStrategy: router.Config_AsIs,
				BalancingRule: []*router.BalancingRule{
					{
						Tag:              "b1",
						OutboundSelector: []string{"test"},
						Strategy:         "champion",
						FallbackTag:      "fall",
					},
				},
			},
		},
	})
}

func TestRouterConfigChampionStrategySettings(t *testing.T) {
	createParser := func() func(string) (proto.Message, error) {
		return func(s string) (proto.Message, error) {
			config := new(RouterConfig)
			if err := json.Unmarshal([]byte(s), config); err != nil {
				return nil, err
			}
			return config.Build()
		}
	}

	runMultiTestCase(t, []TestCase{
		{
			Input: `{
				"balancers": [
					{
						"tag": "b1",
						"selector": ["test"],
						"strategy": {
							"type": "champion",
							"settings": {
								"candidateObservationCount": 5,
								"preferredObservationCount": 8,
								"healthPingJitterScale": 1.5,
								"preferredMaxDelayGap": "120ms",
								"preferredTag": "test-b"
							}
						},
						"fallbackTag": "fall"
					}
				]
			}`,
			Parser: createParser(),
			Output: &router.Config{
				DomainStrategy: router.Config_AsIs,
				BalancingRule: []*router.BalancingRule{
					{
						Tag:              "b1",
						OutboundSelector: []string{"test"},
						Strategy:         "champion",
						StrategySettings: serial.ToTypedMessage(&router.StrategyChampionConfig{
							CandidateObservationCount: 5,
							PreferredObservationCount: 8,
							HealthPingJitterScale:     1.5,
							PreferredMaxDelayGap:      int64(120 * time.Millisecond),
							PreferredTag:              "test-b",
						}),
						FallbackTag: "fall",
					},
				},
			},
		},
	})
}

func TestRouterConfigChampionStrategyPartialSettings(t *testing.T) {
	createParser := func() func(string) (proto.Message, error) {
		return func(s string) (proto.Message, error) {
			config := new(RouterConfig)
			if err := json.Unmarshal([]byte(s), config); err != nil {
				return nil, err
			}
			return config.Build()
		}
	}

	runMultiTestCase(t, []TestCase{
		{
			Input: `{
				"balancers": [
					{
						"tag": "b1",
						"selector": ["test"],
						"strategy": {
							"type": "champion",
							"settings": {
								"candidateObservationCount": 5
							}
						}
					}
				]
			}`,
			Parser: createParser(),
			Output: &router.Config{
				DomainStrategy: router.Config_AsIs,
				BalancingRule: []*router.BalancingRule{
					{
						Tag:              "b1",
						OutboundSelector: []string{"test"},
						Strategy:         "champion",
						StrategySettings: serial.ToTypedMessage(&router.StrategyChampionConfig{
							CandidateObservationCount: 5,
						}),
					},
				},
			},
		},
	})
}

func TestRouterConfigChampionStrategyPreferredTagOnly(t *testing.T) {
	createParser := func() func(string) (proto.Message, error) {
		return func(s string) (proto.Message, error) {
			config := new(RouterConfig)
			if err := json.Unmarshal([]byte(s), config); err != nil {
				return nil, err
			}
			return config.Build()
		}
	}

	runMultiTestCase(t, []TestCase{
		{
			Input: `{
				"balancers": [
					{
						"tag": "b1",
						"selector": ["test"],
						"strategy": {
							"type": "champion",
							"settings": {
								"preferredTag": " test-b "
							}
						}
					}
				]
			}`,
			Parser: createParser(),
			Output: &router.Config{
				DomainStrategy: router.Config_AsIs,
				BalancingRule: []*router.BalancingRule{
					{
						Tag:              "b1",
						OutboundSelector: []string{"test"},
						Strategy:         "champion",
						StrategySettings: serial.ToTypedMessage(&router.StrategyChampionConfig{
							PreferredTag: "test-b",
						}),
					},
				},
			},
		},
	})
}
