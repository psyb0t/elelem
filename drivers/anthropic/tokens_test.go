package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReasoningCounterUsesReplayedBlocks(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(providerReasoningEnvelope{
		Provider: Name, Version: providerReasoningVersion, Model: "source",
		Blocks: []providerReasoningBlock{{
			Index: 0,
			Block: json.RawMessage(
				`{"type":"thinking","thinking":"inspect",
				"signature":"EXAMPLE-DO-NOT-USE"}`),
		}},
	})
	require.NoError(t, err)

	messages := []elelem.Message{{
		Role:    elelem.RoleAssistant,
		Content: elelem.Text("answer"), ProviderReasoning: raw,
	}}
	counter := reasoningTokenCounter{model: elelem.Model{ID: "source"}}
	before, err := counter.Count(messages, nil)
	require.NoError(t, err)

	messages[0].Reasoning = strings.Repeat("display only ", 1000)
	after, err := counter.Count(messages, nil)
	require.NoError(t, err)
	assert.Equal(t, before, after)

	foreign, err := counter.ForModel(elelem.Model{ID: "other"}).
		Count(messages, nil)
	require.NoError(t, err)

	messages[0].ProviderReasoning = nil
	baseline, err := counter.Count(messages, nil)
	require.NoError(t, err)
	assert.Equal(t, baseline, foreign)
	assert.Greater(t, before, baseline)
}
