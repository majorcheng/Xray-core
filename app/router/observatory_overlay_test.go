package router

import (
	"context"
	"testing"

	"github.com/xtls/xray-core/app/observatory"
	"github.com/xtls/xray-core/features/extension"
	"google.golang.org/protobuf/proto"
)

type mutableObservatory struct {
	status []*observatory.OutboundStatus
}

func (m *mutableObservatory) Start() error      { return nil }
func (m *mutableObservatory) Close() error      { return nil }
func (m *mutableObservatory) Type() interface{} { return extension.ObservatoryType() }
func (m *mutableObservatory) GetObservation(context.Context) (proto.Message, error) {
	return &observatory.ObservationResult{Status: m.status}, nil
}

func TestRoundRobinStrategySkipsDeadCandidateFromObservation(t *testing.T) {
	strategy := &RoundRobinStrategy{
		ctx:         context.Background(),
		observatory: &mutableObservatory{status: []*observatory.OutboundStatus{{OutboundTag: "a", Alive: false}, {OutboundTag: "b", Alive: true, Delay: 20}}},
	}
	if got := strategy.PickOutbound([]string{"a", "b"}); got != "b" {
		t.Fatalf("expected round robin to skip dead outbound, got %q", got)
	}
}

func TestRandomStrategySkipsDeadCandidateFromObservation(t *testing.T) {
	strategy := &RandomStrategy{
		ctx:         context.Background(),
		observatory: &mutableObservatory{status: []*observatory.OutboundStatus{{OutboundTag: "a", Alive: false}, {OutboundTag: "b", Alive: true, Delay: 20}}},
	}
	if got := strategy.PickOutbound([]string{"a", "b"}); got != "b" {
		t.Fatalf("expected random strategy to keep only live candidate, got %q", got)
	}
}

func TestLeastPingStrategySkipsDeadCandidateFromObservation(t *testing.T) {
	strategy := &LeastPingStrategy{
		ctx:         context.Background(),
		observatory: &mutableObservatory{status: []*observatory.OutboundStatus{{OutboundTag: "a", Alive: false, Delay: 1}, {OutboundTag: "b", Alive: true, Delay: 80}}},
	}
	if got := strategy.PickOutbound([]string{"a", "b"}); got != "b" {
		t.Fatalf("expected least ping to skip dead outbound, got %q", got)
	}
}

func TestLeastLoadStrategySkipsDeadCandidateFromObservation(t *testing.T) {
	strategy := NewLeastLoadStrategy(&StrategyLeastLoadConfig{})
	strategy.ctx = context.Background()
	strategy.observer = &mutableObservatory{status: []*observatory.OutboundStatus{{OutboundTag: "a", Alive: false, Delay: 10}, {OutboundTag: "b", Alive: true, Delay: 20}}}
	qualified := strategy.getNodes([]string{"a", "b"}, 0)
	if len(qualified) != 1 {
		t.Fatalf("expected 1 qualified outbound, got %d", len(qualified))
	}
	if qualified[0].Tag != "b" {
		t.Fatalf("expected least load to keep outbound b, got %q", qualified[0].Tag)
	}
}
