package atlassianserver

import (
	"github.com/icholy/gritz/internal/server/eventrouter"
)

// RoutedInputs returns the input event of every Route call, in call order.
func (mock *RouterMock) RoutedInputs() []eventrouter.InputEvent {
	var inputs []eventrouter.InputEvent
	for _, call := range mock.RouteCalls() {
		inputs = append(inputs, call.Input)
	}
	return inputs
}
