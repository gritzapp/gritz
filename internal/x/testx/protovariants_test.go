package testx_test

import (
	"testing"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/testx"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

func TestProtoVariants(t *testing.T) {
	events := []*gritzv1.Event{
		{Id: 1, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Version: 1}}},
		{Id: 2},
		{Id: 3, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "go"}}},
	}
	assert.DeepEqual(t, testx.ProtoVariants(events), []proto.Message{
		&gritzv1.CommandPayload{Version: 1},
		&gritzv1.InstructionPayload{Text: "go"},
	}, protocmp.Transform())
}

func TestProtoVariantsOf(t *testing.T) {
	events := []*gritzv1.Event{
		{Id: 1, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Version: 1}}},
		{Id: 2, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "go"}}},
		{Id: 3, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Version: 2}}},
	}
	assert.DeepEqual(t, testx.ProtoVariantsOf[*gritzv1.CommandPayload](events), []*gritzv1.CommandPayload{
		{Version: 1},
		{Version: 2},
	}, protocmp.Transform())
	assert.Assert(t, testx.ProtoVariantsOf[*gritzv1.ReportPayload](events) == nil)
}
