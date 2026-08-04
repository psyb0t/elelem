package anthropic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// KnownModels is public API -- callers use it to populate a model picker
// without a network round trip. An entry that came back without metadata would
// silently disable the context-size checks for whoever selected it.
func TestKnownModels_CarryUsableMetadata(t *testing.T) {
	t.Parallel()

	models := KnownModels()
	require.NotEmpty(t, models)

	seen := make(map[string]struct{}, len(models))

	for _, model := range models {
		assert.NotEmpty(t, model.ID)
		assert.Positive(
			t,
			model.ContextSize,
			"%s must carry a context size or budgeting silently no-ops",
			model.ID,
		)

		_, duplicate := seen[model.ID]
		assert.False(t, duplicate, "duplicate model id %s", model.ID)
		seen[model.ID] = struct{}{}
	}
}

// The catalog is handed out by value; a caller mutating what it got back must
// not corrupt the next caller's copy.
func TestKnownModels_ReturnsACopy(t *testing.T) {
	t.Parallel()

	first := KnownModels()
	require.NotEmpty(t, first)

	original := first[0].ID
	first[0].ID = "mutated"

	assert.Equal(t, original, KnownModels()[0].ID)
}
