package zaicoding

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A caller that stores an elelem.Message as JSON reads a missing envelope back
// as "null". That must count as no reasoning: no extra fields and no mismatch
// warning, which used to fire for every such message on every token count.
//
// Not parallel: the test swaps slog.Default to see what the driver logs.
func TestProviderReasoningExtraTreatsJSONNullAsAbsent(t *testing.T) {
	buf := &bytes.Buffer{}
	previous := slog.Default()

	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	extra, err := providerReasoningExtra(
		t.Context(),
		LookupModel(modelGLM53),
		elelem.Message{
			Role:              elelem.RoleAssistant,
			ProviderReasoning: json.RawMessage("null"),
		},
	)
	require.NoError(t, err)

	assert.Empty(t, extra)
	assert.NotContains(
		t,
		buf.String(),
		elelem.LogReasonProviderReasoningMismatch,
	)
}
