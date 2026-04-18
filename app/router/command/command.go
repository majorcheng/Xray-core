package command

import (
	"context"
	"strings"
	"time"

	routerapp "github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/cmdarg"
	"github.com/xtls/xray-core/common/errors"
	cserial "github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/features/stats"
	"google.golang.org/grpc"
)

// routingServer is an implementation of RoutingService.
type routingServer struct {
	router       routing.Router
	routingStats stats.Channel
}

func (s *routingServer) GetBalancerInfo(ctx context.Context, request *GetBalancerInfoRequest) (*GetBalancerInfoResponse, error) {
	var ret GetBalancerInfoResponse
	ret.Balancer = &BalancerMsg{}
	if bo, ok := s.router.(routing.BalancerOverrider); ok {
		{
			res, err := bo.GetOverrideTarget(request.GetTag())
			if err != nil {
				return nil, err
			}
			ret.Balancer.Override = &OverrideInfo{
				Target: res,
			}
		}
	}

	if pt, ok := s.router.(routing.BalancerPrincipleTarget); ok {
		{
			res, err := pt.GetPrincipleTarget(request.GetTag())
			if err != nil {
				errors.LogInfoInner(ctx, err, "unable to obtain principle target")
			} else {
				ret.Balancer.PrincipleTarget = &PrincipleTargetInfo{Tag: res}
			}
		}
	}
	return &ret, nil
}

func (s *routingServer) OverrideBalancerTarget(ctx context.Context, request *OverrideBalancerTargetRequest) (*OverrideBalancerTargetResponse, error) {
	if bo, ok := s.router.(routing.BalancerOverrider); ok {
		return &OverrideBalancerTargetResponse{}, bo.SetOverrideTarget(request.BalancerTag, request.Target)
	}
	return nil, errors.New("unsupported router implementation")
}

func (s *routingServer) AddRule(ctx context.Context, request *AddRuleRequest) (*AddRuleResponse, error) {
	if bo, ok := s.router.(routing.Router); ok {
		return &AddRuleResponse{}, bo.AddRule(request.Config, request.ShouldAppend)
	}
	return nil, errors.New("unsupported router implementation")

}

func extractRoutingConfigFromCore(config *core.Config) (*routerapp.Config, error) {
	if config == nil {
		return nil, errors.New("config is nil")
	}

	for _, appConfig := range config.App {
		if appConfig == nil {
			continue
		}
		instance, err := appConfig.GetInstance()
		if err != nil {
			return nil, err
		}
		if routingConfig, ok := instance.(*routerapp.Config); ok {
			return routingConfig, nil
		}
	}

	// 未显式配置 router app 时，按空路由表处理，保持与启动时“无路由规则”一致。
	return &routerapp.Config{}, nil
}

func loadRoutingConfigFromFiles(format string, files []string) (*routerapp.Config, error) {
	format = strings.TrimSpace(format)
	if format == "" {
		format = "auto"
	}

	configFiles := make(cmdarg.Arg, 0, len(files))
	for _, file := range files {
		file = strings.TrimSpace(file)
		switch {
		case file == "":
			continue
		case file == "stdin:":
			return nil, errors.New("routing reload via API does not support stdin")
		case strings.Contains(file, "://"):
			return nil, errors.New("routing reload via API does not support remote config URLs: ", file)
		default:
			configFiles = append(configFiles, file)
		}
	}
	if len(configFiles) == 0 {
		return nil, errors.New("no local config files available for routing reload")
	}

	config, err := core.LoadConfig(format, configFiles)
	if err != nil {
		return nil, err
	}
	return extractRoutingConfigFromCore(config)
}

func (s *routingServer) ReloadRoutingConfig(ctx context.Context, request *ReloadRoutingConfigRequest) (*ReloadRoutingConfigResponse, error) {
	if bo, ok := s.router.(routing.Router); ok {
		routingConfig, err := loadRoutingConfigFromFiles(request.GetFormat(), request.GetConfigFiles())
		if err != nil {
			return nil, err
		}
		return &ReloadRoutingConfigResponse{}, bo.AddRule(cserial.ToTypedMessage(routingConfig), false)
	}
	return nil, errors.New("unsupported router implementation")
}

func (s *routingServer) RemoveRule(ctx context.Context, request *RemoveRuleRequest) (*RemoveRuleResponse, error) {
	if bo, ok := s.router.(routing.Router); ok {
		return &RemoveRuleResponse{}, bo.RemoveRule(request.RuleTag)
	}
	return nil, errors.New("unsupported router implementation")
}

func (s *routingServer) ListRule(ctx context.Context, request *ListRuleRequest) (*ListRuleResponse, error) {
	if bo, ok := s.router.(routing.Router); ok {
		response := &ListRuleResponse{}
		for _, v := range bo.ListRule() {
			response.Rules = append(response.Rules, &ListRuleItem{
				Tag:     v.GetOutboundTag(),
				RuleTag: v.GetRuleTag(),
			})
		}
		return response, nil
	}
	return nil, errors.New("unsupported router implementation")
}

// NewRoutingServer creates a statistics service with statistics manager.
func NewRoutingServer(router routing.Router, routingStats stats.Channel) RoutingServiceServer {
	return &routingServer{
		router:       router,
		routingStats: routingStats,
	}
}

func (s *routingServer) TestRoute(ctx context.Context, request *TestRouteRequest) (*RoutingContext, error) {
	if request.RoutingContext == nil {
		return nil, errors.New("Invalid routing request.")
	}
	route, err := s.router.PickRoute(AsRoutingContext(request.RoutingContext))
	if err != nil {
		return nil, err
	}
	if request.PublishResult && s.routingStats != nil {
		ctx, _ := context.WithTimeout(context.Background(), 4*time.Second)
		s.routingStats.Publish(ctx, route)
	}
	return AsProtobufMessage(request.FieldSelectors)(route), nil
}

func (s *routingServer) SubscribeRoutingStats(request *SubscribeRoutingStatsRequest, stream RoutingService_SubscribeRoutingStatsServer) error {
	if s.routingStats == nil {
		return errors.New("Routing statistics not enabled.")
	}
	genMessage := AsProtobufMessage(request.FieldSelectors)
	subscriber, err := stats.SubscribeRunnableChannel(s.routingStats)
	if err != nil {
		return err
	}
	defer stats.UnsubscribeClosableChannel(s.routingStats, subscriber)
	for {
		select {
		case value, ok := <-subscriber:
			if !ok {
				return errors.New("Upstream closed the subscriber channel.")
			}
			route, ok := value.(routing.Route)
			if !ok {
				return errors.New("Upstream sent malformed statistics.")
			}
			err := stream.Send(genMessage(route))
			if err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

func (s *routingServer) mustEmbedUnimplementedRoutingServiceServer() {}

type service struct {
	v *core.Instance
}

func (s *service) Register(server *grpc.Server) {
	common.Must(s.v.RequireFeatures(func(router routing.Router, stats stats.Manager) {
		rs := NewRoutingServer(router, nil)
		RegisterRoutingServiceServer(server, rs)

		// For compatibility purposes
		vCoreDesc := RoutingService_ServiceDesc
		vCoreDesc.ServiceName = "v2ray.core.app.router.command.RoutingService"
		server.RegisterService(&vCoreDesc, rs)
	}, false))
}

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(ctx context.Context, cfg interface{}) (interface{}, error) {
		s := core.MustFromContext(ctx)
		return &service{v: s}, nil
	}))
}
