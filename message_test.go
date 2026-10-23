package elelem

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasProviderReasoning(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		raw  json.RawMessage
		want bool
	}{
		{name: "nil", raw: nil, want: false},
		{name: "empty", raw: json.RawMessage{}, want: false},
		{name: "blank", raw: json.RawMessage(" \n\t"), want: false},
		{name: "JSON null", raw: json.RawMessage("null"), want: false},
		{name: "padded null", raw: json.RawMessage(" null\n"), want: false},
		{name: "envelope", raw: json.RawMessage(`{"a":"x"}`), want: true},
		{name: "empty object", raw: json.RawMessage(`{}`), want: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, HasProviderReasoning(tc.raw))
		})
	}
}

// A Message has no JSON tags, so storing one without reasoning and reading it
// back yields ProviderReasoning == "null". Cloning drops it, so the engine
// never hands drivers a value they would decode as an envelope.
func TestMessageCloneDropsNullReasoningAfterJSONRoundTrip(t *testing.T) {
	t.Parallel()

	//nolint:musttag // The untagged round trip is the case under test.
	encoded, err := json.Marshal(Message{
		Role:    RoleAssistant,
		Content: Text("answer"),
	})
	require.NoError(t, err)

	var decoded Message
	//nolint:musttag // The untagged round trip is the case under test.
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, json.RawMessage("null"), decoded.ProviderReasoning)

	cloned := decoded.clone()

	assert.Nil(t, cloned.ProviderReasoning)
	assert.Equal(t, "answer", cloned.Text())
}

func TestMessageCloneKeepsProviderReasoning(t *testing.T) {
	t.Parallel()

	original := Message{
		Role:              RoleAssistant,
		ProviderReasoning: json.RawMessage(`{"provider":"x"}`),
	}

	cloned := original.clone()
	original.ProviderReasoning[2] = 'X'

	assert.JSONEq(t, `{"provider":"x"}`, string(cloned.ProviderReasoning))
}
