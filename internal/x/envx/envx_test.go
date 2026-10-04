package envx

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestExpand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		s    string
		want string
	}{
		{"present", "$A", "1"},
		{"braces", "${A}/x", "1/x"},
		{"last wins", "$B", "3"},
		{"value with equals", "$C", "x=y"},
		{"missing", "$D", ""},
		{"prefix is not a match", "$AB", ""},
		{"no variables", "plain", "plain"},
	}
	env := []string{"A=1", "B=2", "B=3", "C=x=y", "ABC=4"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, Expand(tt.s, env), tt.want)
		})
	}
}

func TestFromMap(t *testing.T) {
	t.Parallel()
	assert.DeepEqual(t, FromMap(map[string]string{"B": "2", "A": "x=y", "C": ""}), []string{"A=x=y", "B=2", "C="})
	assert.DeepEqual(t, FromMap(nil), []string{})
}
