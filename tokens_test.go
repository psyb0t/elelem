package elelem

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SetDefaultTokenCounter mutates process-wide state, so this test is
// deliberately NOT parallel and restores the previous counter on cleanup.
func TestSetDefaultTokenCounter(t *testing.T) {
	const replacement = 77

	original := DefaultTokenCounter()

	t.Cleanup(func() { SetDefaultTokenCounter(original) })

	SetDefaultTokenCounter(fixedCounter(replacement))

	count, err := DefaultTokenCounter().
		Count([]Message{{Role: RoleUser}}, nil)
	require.NoError(t, err)
	assert.Equal(t, replacement, count)

	// Documented behaviour: nil RESETS to the built-in estimator rather than
	// leaving the previously installed counter in place.
	SetDefaultTokenCounter(nil)
	assert.IsType(t, builtInTokenCounter{}, DefaultTokenCounter())
}

// The built-in estimator is the last fallback, so nothing else in the suite
// exercises it -- every other test installs a counter of its own.
func TestBuiltInTokenCounter_Count(t *testing.T) {
	t.Parallel()

	counter := builtInTokenCounter{}

	empty, err := counter.Count(nil, nil)
	require.NoError(t, err)

	withContent, err := counter.Count([]Message{{
		Role:    RoleUser,
		Content: Text("a sentence with several words in it"),
	}}, nil)
	require.NoError(t, err)
	assert.Greater(t, withContent, empty)

	// Reasoning rides the wire back to the provider, so it must be counted;
	// omitting it made the budget undercount every reasoning transcript.
	withReasoning, err := counter.Count([]Message{{
		Role:      RoleAssistant,
		Content:   Text("answer"),
		Reasoning: "a long chain of thought that costs real tokens",
	}}, nil)
	require.NoError(t, err)

	withoutReasoning, err := counter.Count([]Message{{
		Role:    RoleAssistant,
		Content: Text("answer"),
	}}, nil)
	require.NoError(t, err)
	assert.Greater(t, withReasoning, withoutReasoning)

	// Tool schemas are part of the prompt the provider bills for.
	withTools, err := counter.Count(nil, []Tool{{
		Name:        "lookup",
		Description: "look something up",
	}})
	require.NoError(t, err)
	assert.Greater(t, withTools, empty)
}

// countingDriver is a scriptedDriver whose TokenCounter answers a fixed,
// non-zero count -- the scripted one answers 0, which cannot distinguish
// "the driver's counter was used" from "no counter counted anything".
type countingDriver struct {
	scriptedDriver

	count int
}

func (d *countingDriver) TokenCounter() TokenCounter {
	return fixedCounter(d.count)
}
