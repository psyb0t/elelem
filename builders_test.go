package elelem

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fluent builders are the package's entire public surface for configuring
// a request, and each one fails silently if it writes the wrong field:
// nothing errors, the provider simply receives a request that is not the one
// the caller described. These pin every setter to the field it names.
func TestRequestBuilders_LandOnTheFieldTheyName(t *testing.T) {
	t.Parallel()

	const (
		temperature      = 0.25
		topP             = 0.9
		frequencyPenalty = 0.5
		presencePenalty  = 0.75
		seed             = int64(42)
		timeout          = 3 * time.Second
		schemaName       = "incident"
	)

	schema := json.RawMessage(`{"type":"object"}`)

	testCases := []struct {
		name  string
		build func(*Request) *Request
		check func(*testing.T, *Request)
	}{
		{
			name: "WithSystemMessagef formats",
			build: func(r *Request) *Request {
				return r.WithSystemMessagef("hello %s", "world")
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Equal(t, "hello world", r.baseSystemMessage)
			},
		},
		{
			name: "WithSystemMessageAppendf formats and appends",
			build: func(r *Request) *Request {
				return r.WithSystemMessageAppendf("n=%d", 1)
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Equal(t, []string{"n=1"}, r.systemMessageAppends)
			},
		},
		{
			name: "WithSystemMessageAppendReset clears appends",
			build: func(r *Request) *Request {
				return r.
					WithSystemMessageAppend("first").
					WithSystemMessageAppendReset()
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Empty(t, r.systemMessageAppends)
			},
		},
		{
			name: "WithTools replaces the whole set",
			build: func(r *Request) *Request {
				return r.
					WithTool(Tool{Name: "replaced"}).
					WithTools(NewToolSet(Tool{Name: "kept"}))
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.tools)

				definitions := r.tools.Definitions()

				names := make([]string, 0, len(definitions))
				for _, tool := range definitions {
					names = append(names, tool.Name)
				}

				assert.Equal(t, []string{"kept"}, names)
			},
		},
		{
			name: "WithGenerationParams clones the whole block",
			build: func(r *Request) *Request {
				value := temperature

				return r.WithGenerationParams(GenerationParams{
					Temperature: &value,
				})
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.Temperature)
				assert.InDelta(t, temperature, *r.params.Temperature, 0)
			},
		},
		{
			name: "WithTemperature",
			build: func(r *Request) *Request {
				return r.WithTemperature(temperature)
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.Temperature)
				assert.InDelta(t, temperature, *r.params.Temperature, 0)
			},
		},
		{
			name:  "WithTopP",
			build: func(r *Request) *Request { return r.WithTopP(topP) },
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.TopP)
				assert.InDelta(t, topP, *r.params.TopP, 0)
			},
		},
		{
			name:  "WithSeed",
			build: func(r *Request) *Request { return r.WithSeed(seed) },
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.Seed)
				assert.Equal(t, seed, *r.params.Seed)
			},
		},
		{
			name: "WithStop copies the caller's slice",
			build: func(r *Request) *Request {
				return r.WithStop("a", "b")
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Equal(t, []string{"a", "b"}, r.params.Stop)
			},
		},
		{
			name: "WithFrequencyPenalty",
			build: func(r *Request) *Request {
				return r.WithFrequencyPenalty(frequencyPenalty)
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.FrequencyPenalty)
				assert.InDelta(
					t, frequencyPenalty, *r.params.FrequencyPenalty, 0,
				)
			},
		},
		{
			name: "WithPresencePenalty",
			build: func(r *Request) *Request {
				return r.WithPresencePenalty(presencePenalty)
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.PresencePenalty)
				assert.InDelta(
					t, presencePenalty, *r.params.PresencePenalty, 0,
				)
			},
		},
		{
			name:  "WithJSONMode",
			build: func(r *Request) *Request { return r.WithJSONMode() },
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.ResponseFormat)
				assert.Equal(
					t,
					ResponseFormatTypeJSONObject,
					r.params.ResponseFormat.Type,
				)
			},
		},
		{
			name: "WithJSONSchema",
			build: func(r *Request) *Request {
				return r.WithJSONSchema(schemaName, schema, true)
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.ResponseFormat)
				assert.Equal(
					t,
					ResponseFormatTypeJSONSchema,
					r.params.ResponseFormat.Type,
				)
				assert.Equal(t, schemaName, r.params.ResponseFormat.Name)
				assert.True(t, r.params.ResponseFormat.StrictSchema)
				assert.JSONEq(
					t,
					string(schema),
					string(r.params.ResponseFormat.Schema),
				)
			},
		},
		{
			name: "WithParam allocates the extra map",
			build: func(r *Request) *Request {
				return r.WithParam("k", "v")
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Equal(t, "v", r.params.Extra["k"])
			},
		},
		{
			name: "WithParams merges into the extra map",
			build: func(r *Request) *Request {
				return r.
					WithParam("first", 1).
					WithParams(map[string]any{"second": 2})
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Equal(t, 1, r.params.Extra["first"])
				assert.Equal(t, 2, r.params.Extra["second"])
			},
		},
		{
			name:  "WithTimeout",
			build: func(r *Request) *Request { return r.WithTimeout(timeout) },
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Equal(t, timeout, r.timeout)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := NewRequest(New(&scriptedDriver{}))
			tc.check(t, tc.build(request))
		})
	}
}

// WithJSONSchema and WithStop both copy the caller's slice. If either aliased
// it, a caller reusing its own buffer after building the request would be
// mutating an in-flight request from the outside.
func TestRequestBuilders_CopyCallerOwnedSlices(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object"}`)
	stop := []string{"halt"}

	request := NewRequest(New(&scriptedDriver{})).
		WithJSONSchema("n", schema, false).
		WithStop(stop...)

	schema[0] = 'X'
	stop[0] = "mutated"

	require.NotNil(t, request.params.ResponseFormat)
	assert.JSONEq(
		t,
		`{"type":"object"}`,
		string(request.params.ResponseFormat.Schema),
	)
	assert.Equal(t, []string{"halt"}, request.params.Stop)
}

func TestRequest_ParallelToolCallsEnabled(t *testing.T) {
	t.Parallel()

	enabled := true
	disabled := false

	testCases := []struct {
		name  string
		value *bool
		want  bool
	}{
		{name: "unset is not enabled", value: nil, want: false},
		{name: "explicitly false", value: &disabled, want: false},
		{name: "explicitly true", value: &enabled, want: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := NewRequest(New(&scriptedDriver{}))
			request.params.ParallelToolCalls = tc.value

			assert.Equal(t, tc.want, request.parallelToolCallsEnabled())
		})
	}
}

// IsTokenLimitReached is the caller's pre-flight check. The load-bearing case
// is the unresolvable budget: a model carrying no ContextSize and no explicit
// cap must report false rather than a limit it cannot compute.
func TestRequest_IsTokenLimitReached(t *testing.T) {
	t.Parallel()

	const (
		tinyBudget  = 1
		largeBudget = 1_000_000
	)

	t.Run("no resolvable budget reports false", func(t *testing.T) {
		t.Parallel()

		request := NewRequest(New(&scriptedDriver{})).WithPrompt("hi")

		reached, err := request.IsTokenLimitReached()
		require.NoError(t, err)
		assert.False(t, reached)
	})

	t.Run("under a generous budget reports false", func(t *testing.T) {
		t.Parallel()

		request := NewRequest(New(&scriptedDriver{})).
			WithPrompt("hi").
			WithMaxContextTokens(largeBudget)

		reached, err := request.IsTokenLimitReached()
		require.NoError(t, err)
		assert.False(t, reached)
	})

	// The scripted driver's own counter answers 0 for everything, so this
	// case installs the real estimator at the client tier -- which is also
	// the only tier the driver cannot override.
	t.Run("over a tiny budget reports true", func(t *testing.T) {
		t.Parallel()

		client := New(
			&scriptedDriver{},
			WithClientTokenCounter(builtInTokenCounter{}),
		)

		request := NewRequest(client).
			WithSystemMessage("a reasonably long system prompt here").
			WithPrompt("and a prompt on top of it").
			WithMaxContextTokens(tinyBudget)

		reached, err := request.IsTokenLimitReached()
		require.NoError(t, err)
		assert.True(t, reached)
	})
}

// The counter resolution order is request → client → driver → package default
// → built-in, and every tier that silently loses to the wrong neighbour would
// change every budget decision the engine makes without failing anything.
func TestRequest_CounterResolutionOrder(t *testing.T) {
	t.Parallel()

	const (
		requestCount = 11
		clientCount  = 22
		driverCount  = 33
	)

	// fixedCounter scales by message count, so an empty transcript would
	// report 0 from every tier and prove nothing about which one answered.
	oneMessage := []Message{{Role: RoleUser, Content: "hi"}}

	t.Run("request beats client", func(t *testing.T) {
		t.Parallel()

		client := New(
			&scriptedDriver{},
			WithClientTokenCounter(fixedCounter(clientCount)),
		)
		request := NewRequest(client).
			WithTokenCounter(fixedCounter(requestCount))

		count, err := request.resolvedCounter().Count(oneMessage, nil)
		require.NoError(t, err)
		assert.Equal(t, requestCount, count)
	})

	t.Run("client beats driver", func(t *testing.T) {
		t.Parallel()

		client := New(
			&scriptedDriver{},
			WithClientTokenCounter(fixedCounter(clientCount)),
		)

		count, err := NewRequest(client).
			resolvedCounter().Count(oneMessage, nil)
		require.NoError(t, err)
		assert.Equal(t, clientCount, count)
	})

	t.Run("driver is used when nothing overrides it", func(t *testing.T) {
		t.Parallel()

		client := New(&countingDriver{count: driverCount})

		count, err := NewRequest(client).
			resolvedCounter().Count(oneMessage, nil)
		require.NoError(t, err)
		assert.Equal(t, driverCount, count)
	})
}

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
		Content: "a sentence with several words in it",
	}}, nil)
	require.NoError(t, err)
	assert.Greater(t, withContent, empty)

	// Reasoning rides the wire back to the provider, so it must be counted;
	// omitting it made the budget undercount every reasoning transcript.
	withReasoning, err := counter.Count([]Message{{
		Role:      RoleAssistant,
		Content:   "answer",
		Reasoning: "a long chain of thought that costs real tokens",
	}}, nil)
	require.NoError(t, err)

	withoutReasoning, err := counter.Count([]Message{{
		Role:    RoleAssistant,
		Content: "answer",
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

func TestClient_DriverAccessor(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{}
	client := New(driver)

	assert.Same(t, driver, client.Driver())

	// Documented behaviour: a nil Client answers nil rather than panicking,
	// so a caller composing decorators need not nil-check at every site.
	var nilClient *Client

	assert.Nil(t, nilClient.Driver())
}

func TestModel_IsReasoning(t *testing.T) {
	t.Parallel()

	assert.True(t, Model{SupportsReasoning: true}.IsReasoning())
	assert.False(t, Model{}.IsReasoning())
}

func TestUsage_BilledTotalTokensIncludesWastedRetries(t *testing.T) {
	t.Parallel()

	const (
		succeeded = int64(100)
		wasted    = int64(30)
	)

	usage := Usage{
		TokenCounts: TokenCounts{Total: succeeded},
		Retry:       RetryInfo{WastedTotalTokens: wasted},
	}

	assert.Equal(t, succeeded, usage.Total)
	assert.Equal(t, succeeded+wasted, usage.BilledTotalTokens())
}
