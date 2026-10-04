package mcpbridge

import (
	"github.com/gritzapp/gritz/internal/x/mcpchannel"
)

// SentChannelParams returns the params of every SendChannel call, in call order.
func (mock *ChannelSenderMock) SentChannelParams() []mcpchannel.Params {
	var params []mcpchannel.Params
	for _, call := range mock.SendChannelCalls() {
		params = append(params, call.P)
	}
	return params
}
