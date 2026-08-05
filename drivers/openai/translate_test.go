package openai

import (
	"encoding/json"
	"strings"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A refusal arrives on its own field INSTEAD of content. Dropping it made a
// refused structured-output request indistinguishable from a clean empty
// answer — no text, FinishReasonStop, no error — which sends the operator to
// debug a schema bug that does not exist.
func TestDeltasFromChunkSurfacesRefusal(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		refusal  string
		content  string
		wantText []string
	}{
		{
			name:     "refusal alone is surfaced",
			refusal:  "I cannot comply with that request.",
			wantText: []string{"I cannot comply with that request."},
		},
		{
			name:     "content alone is unaffected",
			content:  "hello",
			wantText: []string{"hello"},
		},
		{
			name:     "neither yields no text delta",
			wantText: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			chunk := openaisdk.ChatCompletionChunk{
				Choices: []openaisdk.ChatCompletionChunkChoice{{
					Delta: openaisdk.ChatCompletionChunkChoiceDelta{
						Content: tc.content,
						Refusal: tc.refusal,
					},
				}},
			}

			var text []string

			refused := chunk.Choices[0].Delta.Refusal != ""

			for _, delta := range deltasFromChunk(chunk, refused) {
				if delta.Text != "" {
					text = append(text, delta.Text)
				}
			}

			assert.Equal(t, tc.wantText, text)
		})
	}
}

// A refusal terminates with `stop`, so text alone leaves it indistinguishable
// from a normal answer at the FinishReason — while Anthropic maps its refusal
// to ContentFilter. Identical caller code must not classify the same event
// differently per provider.
func TestRefusalIsClassifiedNotJustSurfaced(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		refusal string
		content string
		raw     string
		want    elelem.FinishReason
	}{
		{
			name:    "refusal promotes stop to content filter",
			refusal: "I cannot help with that.",
			raw:     "stop",
			want:    elelem.FinishReasonContentFilter,
		},
		{
			name:    "ordinary stop is untouched",
			content: "hello",
			raw:     "stop",
			want:    elelem.FinishReasonStop,
		},
		{
			// Never mask a more specific reason with the promotion.
			name:    "length is not overwritten",
			refusal: "partial refusal",
			raw:     "length",
			want:    elelem.FinishReasonLength,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			chunk := openaisdk.ChatCompletionChunk{
				Choices: []openaisdk.ChatCompletionChunkChoice{{
					FinishReason: tc.raw,
					Delta: openaisdk.ChatCompletionChunkChoiceDelta{
						Content: tc.content,
						Refusal: tc.refusal,
					},
				}},
			}

			var got elelem.FinishReason

			refused := chunk.Choices[0].Delta.Refusal != ""

			for _, delta := range deltasFromChunk(chunk, refused) {
				if delta.FinishReason != elelem.FinishReasonUnset {
					got = delta.FinishReason
				}
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

// Usage.FinishReason is the AUTHORITATIVE one — it is what the engine records
// and what callers inspect. usageFromChunk used to normalize the reason
// independently of the delta path, so the refusal promotion reached the stream
// and not the Usage: a refusal still looked like a clean stop everywhere it
// mattered. Both paths now read one helper, so they cannot drift apart.
func TestRefusalPromotionReachesUsageNotJustDeltas(t *testing.T) {
	t.Parallel()

	chunk := openaisdk.ChatCompletionChunk{
		Choices: []openaisdk.ChatCompletionChunkChoice{{
			FinishReason: "stop",
			Delta: openaisdk.ChatCompletionChunkChoiceDelta{
				Refusal: "I cannot help with that.",
			},
		}},
	}

	refused := chunk.Choices[0].Delta.Refusal != ""

	usage := usageFromChunk(elelem.Usage{}, chunk, refused)
	assert.Equal(t, elelem.FinishReasonContentFilter, usage.FinishReason)

	// And the two paths must agree, which is the actual invariant.
	var streamed elelem.FinishReason

	for _, delta := range deltasFromChunk(chunk, refused) {
		if delta.FinishReason != elelem.FinishReasonUnset {
			streamed = delta.FinishReason
		}
	}

	assert.Equal(t, usage.FinishReason, streamed,
		"delta stream and Usage disagree on the finish reason")
}

// END-TO-END, through a real SSE stream, because the unit-level assertion
// cannot catch this class of bug.
//
// In an actual stream the refusal and the finish_reason arrive on DIFFERENT
// chunks — the terminating chunk carries an empty delta (see stream.sse). A
// chunk-scoped promotion therefore never fires, and a hand-built single chunk
// carrying both fields is a shape the provider never sends: a test constructed
// so it can only pass. Real cost of the miss: a refused CompleteInto is not
// classified, so the repair round fires — a second billed round-trip re-asking
// a model that refused, reported as a schema mismatch that does not exist.
func TestRefusalPromotionSurvivesRealChunkBoundaries(t *testing.T) {
	t.Parallel()

	server := fixtureServer(t, "testdata/refusal_stream.sse")
	driver := NewDriver(
		WithBaseURL(server.URL),
		WithAPIKey(testAPIKey),
		WithHTTPClient(server.Client()),
	)

	var streamed []elelem.Delta

	usage, err := driver.Stream(
		t.Context(),
		elelem.DriverRequest{
			Model: elelem.Model{ID: testModel},
			Messages: []elelem.Message{{
				Role:    elelem.RoleUser,
				Content: elelem.Text("something disallowed"),
			}},
		},
		func(delta elelem.Delta) error {
			streamed = append(streamed, delta)

			return nil
		},
	)
	require.NoError(t, err)

	assert.Equal(t, elelem.FinishReasonContentFilter, usage.FinishReason,
		"refusal split across chunks was not classified")

	// The DELTA channel too. An earlier version of this test collected these
	// deltas and then asserted only on .Text — the contradicting evidence was
	// sitting unread in this very slice while the test reported success.
	var streamedReason elelem.FinishReason

	for _, delta := range streamed {
		if delta.FinishReason != elelem.FinishReasonUnset {
			streamedReason = delta.FinishReason
		}
	}

	assert.Equal(t, usage.FinishReason, streamedReason,
		"delta stream and Usage disagree on the finish reason")

	var text strings.Builder

	for _, delta := range streamed {
		text.WriteString(delta.Text)
	}

	assert.Contains(t, text.String(), "I cannot help with that.",
		"the refusal reason must still reach the caller")
}

// Model capture must not depend on a usage frame arriving.
//
// The existing fixture emits one, so it cannot tell the two placements apart —
// moving the capture back below the token-count guard left the suite green.
// The reachable case has no fixture at all: an OpenAI-compatible endpoint
// (WithBaseURL) that names the model on every chunk and never sends usage.
// Without this, Response.Model comes back empty and the operator cannot tell
// who served the request.
func TestUsageModelIsCapturedWithoutAUsageFrame(t *testing.T) {
	t.Parallel()

	server := fixtureServer(t, "testdata/no_usage_stream.sse")
	driver := NewDriver(
		WithBaseURL(server.URL),
		WithAPIKey(testAPIKey),
		WithHTTPClient(server.Client()),
	)

	usage, err := driver.Stream(
		t.Context(),
		elelem.DriverRequest{
			Model: elelem.Model{ID: testModel},
			Messages: []elelem.Message{{
				Role:    elelem.RoleUser,
				Content: elelem.Text("hi"),
			}},
		},
		func(elelem.Delta) error { return nil },
	)
	require.NoError(t, err)

	assert.Equal(t, testModel, usage.Model,
		"model was lost because the stream carried no usage frame")
	assert.Equal(t, elelem.FinishReasonStop, usage.FinishReason)
	assert.Zero(t, usage.Total, "no usage frame means no token counts")
}

// total_tokens is the DERIVED field, so it is precisely the one an
// OpenAI-compatible endpoint omits. Gating the whole TokenCounts on it threw
// away a frame reporting prompt=120/completion=40 and returned all zeros: no
// usage, no cost, a blind budget, and no error or log saying so.
func TestUsageSurvivesAMissingTotalTokens(t *testing.T) {
	t.Parallel()

	server := fixtureServer(t, "testdata/partial_usage_stream.sse")
	driver := NewDriver(
		WithBaseURL(server.URL),
		WithAPIKey(testAPIKey),
		WithHTTPClient(server.Client()),
	)

	usage, err := driver.Stream(
		t.Context(),
		elelem.DriverRequest{
			Model: elelem.Model{ID: testModel},
			Messages: []elelem.Message{{
				Role:    elelem.RoleUser,
				Content: elelem.Text("hi"),
			}},
		},
		func(elelem.Delta) error { return nil },
	)
	require.NoError(t, err)

	assert.Equal(t, int64(120), usage.Prompt,
		"prompt tokens were discarded with the missing total")
	assert.Equal(t, int64(40), usage.Completion)

	// Derived rather than reported as zero — a zero Total means a zero Cost
	// and a budget that never trips.
	assert.Equal(t, int64(160), usage.Total,
		"Total was not derived from the reported counts")
}

// The `-chat-latest` aliases carry a reasoning-family prefix but are the
// NON-reasoning chat variant. Treating them as reasoning refused temperature
// locally on models whose whole purpose is ordinary chat.
func TestChatLatestAliasesAreNotReasoningModels(t *testing.T) {
	t.Parallel()

	temperature := 0.5

	for _, id := range []string{
		"gpt-5-chat-latest",
		"gpt-5.1-chat-latest",
		"gpt-5.2-chat-latest",
		"gpt-5.3-chat-latest",
	} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			caps := NewDriver().Capabilities(LookupModel(id))
			assert.True(t, caps.SupportsSamplingParams,
				"%s: a chat alias must accept sampling", id)

			request := elelem.DriverRequest{Model: LookupModel(id)}
			request.Params.Temperature = &temperature

			_, err := toOpenAIParams(request)
			assert.NoError(t, err, "%s: temperature refused locally", id)
		})
	}

	// The reasoning models themselves must still refuse it.
	request := elelem.DriverRequest{Model: LookupModel(modelGPT56)}
	request.Params.Temperature = &temperature

	_, err := toOpenAIParams(request)
	require.Error(t, err, "a reasoning model must still refuse temperature")
}

// The early-o1 generation predates function calling and structured outputs
// alike, so claiming either for it is an unbacked assertion.
func TestEarlyO1ClaimsNoToolSupport(t *testing.T) {
	t.Parallel()

	driver := NewDriver()

	for _, id := range []string{"o1-mini", "o1-preview", "o1-mini-2024-09-12"} {
		caps := driver.Capabilities(elelem.Model{ID: id})

		assert.False(t, caps.SupportsToolChoice, "%s tool choice", id)
		assert.False(t, caps.SupportsParallelToolCalls, "%s parallel", id)
	}

	// The base o1 DID get both — the restriction must not leak to it.
	caps := driver.Capabilities(elelem.Model{ID: modelO1})
	require.True(t, caps.SupportsToolChoice)
	require.True(t, caps.SupportsParallelToolCalls)
}

// The driver never populates prompt_cache_options, so a CacheHint is dropped
// and implicit caching applies. The flag describes what the caller actually
// gets, so it must say false regardless of what the API could support.
func TestPromptCachingReportsDriverBehaviourNotAPICapability(t *testing.T) {
	t.Parallel()

	driver := NewDriver()

	for _, id := range []string{
		modelGPT56,
		modelGPT56Sol,
		modelGPT4o,
		"some-unknown-endpoint-model",
	} {
		caps := driver.Capabilities(elelem.Model{ID: id})
		assert.False(t, caps.SupportsPromptCaching,
			"%s: claims breakpoints the driver never sends", id)
	}
}

// An assistant turn carrying tool calls is the hinge of the whole tool loop:
// it is what gets replayed to the provider on the next round. Getting it wrong
// does not fail here -- it fails on the FOLLOWING request, as a provider
// rejection at a call site that did nothing wrong.
func TestToOpenAIMessages_AssistantWithToolCalls(t *testing.T) {
	t.Parallel()

	messages, err := toOpenAIMessages([]elelem.Message{{
		Role:    elelem.RoleAssistant,
		Content: elelem.Text("calling a tool"),
		ToolCalls: []elelem.ToolCall{{
			ID:        "call-1",
			Name:      "lookup",
			Arguments: json.RawMessage(`{"q":"x"}`),
		}},
	}})
	require.NoError(t, err)
	require.Len(t, messages, 1)

	assistant := messages[0].OfAssistant
	require.NotNil(t, assistant, "must translate to an assistant param")
	assert.Equal(t, "calling a tool", assistant.Content.OfString.Value)
	require.Len(t, assistant.ToolCalls, 1)

	function := assistant.ToolCalls[0].OfFunction
	require.NotNil(t, function)
	assert.Equal(t, "call-1", function.ID)
	assert.Equal(t, "lookup", function.Function.Name)
	assert.JSONEq(t, `{"q":"x"}`, function.Function.Arguments)
}

// A tool call with no arguments is normal -- a zero-parameter tool produces
// one. The wire form is not: an empty arguments string is not valid JSON, so
// it has to go out as an empty object.
func TestToOpenAIMessages_EmptyToolCallArgumentsBecomeAnObject(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		arguments json.RawMessage
	}{
		{name: "nil", arguments: nil},
		{name: "empty", arguments: json.RawMessage(``)},
		{name: "whitespace only", arguments: json.RawMessage("  \n\t")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			messages, err := toOpenAIMessages([]elelem.Message{{
				Role: elelem.RoleAssistant,
				ToolCalls: []elelem.ToolCall{{
					ID:        "call-1",
					Name:      "no_args",
					Arguments: tc.arguments,
				}},
			}})
			require.NoError(t, err)
			require.Len(t, messages, 1)

			require.NotNil(t, messages[0].OfAssistant)
			require.Len(t, messages[0].OfAssistant.ToolCalls, 1)

			function := messages[0].OfAssistant.ToolCalls[0].OfFunction
			require.NotNil(t, function)
			assert.JSONEq(t, `{}`, function.Function.Arguments)
		})
	}
}

// The no-tool-calls branch takes a different path entirely, and must not
// produce the tool-call-carrying shape.
func TestToOpenAIMessages_AssistantWithoutToolCalls(t *testing.T) {
	t.Parallel()

	messages, err := toOpenAIMessages([]elelem.Message{{
		Role:    elelem.RoleAssistant,
		Content: elelem.Text("plain answer"),
	}})
	require.NoError(t, err)
	require.Len(t, messages, 1)

	assistant := messages[0].OfAssistant
	require.NotNil(t, assistant)
	assert.Empty(t, assistant.ToolCalls)
	assert.Equal(t, "plain answer", assistant.Content.OfString.Value)
}
