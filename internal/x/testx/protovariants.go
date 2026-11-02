package testx

import "google.golang.org/protobuf/proto"

// ProtoVariants returns the value of every set oneof arm in msgs, in order.
// Arms whose value is not a message (scalars, enums) are skipped. It flattens
// a slice of union-carrying messages, such as task events, into their
// payloads so they can be asserted whole with protocmp.Transform:
//
//	assert.DeepEqual(t, testx.ProtoVariants(events), []proto.Message{
//		&gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1},
//	}, protocmp.Transform())
func ProtoVariants[M proto.Message](msgs []M) []proto.Message {
	var variants []proto.Message
	for _, msg := range msgs {
		m := msg.ProtoReflect()
		oneofs := m.Descriptor().Oneofs()
		for i := range oneofs.Len() {
			fd := m.WhichOneof(oneofs.Get(i))
			if fd == nil || fd.Message() == nil {
				continue
			}
			variants = append(variants, m.Get(fd).Message().Interface())
		}
	}
	return variants
}

// ProtoVariantsOf returns the oneof arm values in msgs whose type is T, in
// order. It is ProtoVariants filtered to one variant type:
//
//	assert.DeepEqual(t, testx.ProtoVariantsOf[*gritzv1.CommandPayload](events), []*gritzv1.CommandPayload{
//		{Command: gritzv1.TaskCommand_START, Version: 1},
//	}, protocmp.Transform())
func ProtoVariantsOf[T proto.Message, M proto.Message](msgs []M) []T {
	var variants []T
	for _, v := range ProtoVariants(msgs) {
		if v, ok := v.(T); ok {
			variants = append(variants, v)
		}
	}
	return variants
}
