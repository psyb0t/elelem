package elelem

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestModel_IsReasoning(t *testing.T) {
	t.Parallel()

	assert.True(t, Model{SupportsReasoning: true}.IsReasoning())
	assert.False(t, Model{}.IsReasoning())
}
