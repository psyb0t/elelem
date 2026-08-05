// Package conformance is the contract suite for people WRITING an
// elelem.Driver. Run checks an implementation against the behaviour the engine
// relies on: cancellation, local transcript validation, delta order, usage
// invariants, normalized finish reasons, and that every advertised capability
// is actually enforced rather than merely claimed.
//
// It is a subpackage rather than part of elelemtest because it imports
// `testing` and testify. elelemtest itself is reachable from production code
// (the app resolves every upstream to the scripted driver under `go test`), and
// a package that imports elelemtest must not drag the test framework into the
// shipped binary.
//
// For the doubles rather than the contract — a scripted Driver, or a generated
// mock — see elelemtest and elelemtest/mocks.
package conformance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	capabilityTestValue = 0.25
	capabilityTestSeed  = int64(42)
)

// Options configures Run for a driver's fake provider endpoint.
type Options struct {
	Request        elelem.DriverRequest
	SkipListModels bool
	NetworkCalls   func() int64

	// Models re-runs the capability contract against each of these in place of
	// Request.Model. Capabilities is a PER-MODEL function, so a suite pinned to
	// one model cannot see a driver that gates correctly for the model it was
	// handed and wrongly for its siblings — which is exactly where the real
	// bugs live. Leave empty to check Request.Model alone.
	Models []elelem.Model
}

// capabilityModels returns every model the capability contract should cover.
func (o Options) capabilityModels() []elelem.Model {
	if len(o.Models) == 0 {
		return []elelem.Model{o.Request.Model}
	}

	return o.Models
}

type capabilityCase struct {
	name      string
	supported func(elelem.Capabilities) bool
	configure func(*elelem.DriverRequest)
}

// Run checks the provider-neutral behavior required by elelem's
// engine. The supplied endpoint must return a valid stream for repeated calls.
func Run(
	t *testing.T,
	newDriver func() elelem.Driver,
	options Options,
) {
	t.Helper()

	// These subtests deliberately do NOT call t.Parallel(), against the usual
	// convention. NetworkCalls is a single shared counter and the
	// transcript/capability cases assert on its delta across one call — running
	// them concurrently would interleave increments and make those assertions
	// meaningless. Serial is a correctness requirement here, not an oversight.
	t.Run("stream contract", func(t *testing.T) {
		runStreamContract(t, newDriver, options.Request)
	})

	if !options.SkipListModels {
		t.Run("model listing", func(t *testing.T) {
			runModelListing(t, newDriver)
		})
	}

	t.Run("canceled context", func(t *testing.T) {
		runCanceledContext(t, newDriver, options.Request)
	})

	t.Run("invalid transcript is local", func(t *testing.T) {
		runInvalidTranscript(t, newDriver, options)
	})

	t.Run("capabilities are honest", func(t *testing.T) {
		runCapabilityContract(t, newDriver, options)
	})
}

func runStreamContract(
	t *testing.T,
	newDriver func() elelem.Driver,
	request elelem.DriverRequest,
) {
	t.Helper()
	driver := requireDriver(t, newDriver)

	var deltas []elelem.Delta

	usage, err := driver.Stream(
		t.Context(),
		cloneRequest(request),
		func(delta elelem.Delta) error {
			deltas = append(deltas, delta)

			return nil
		},
	)
	require.NoError(t, err)
	assertDeltaContract(t, deltas)
	assertUsageContract(t, usage)
	assertFinishReasonsAgree(t, deltas, usage)
}

func runModelListing(t *testing.T, newDriver func() elelem.Driver) {
	t.Helper()
	driver := requireDriver(t, newDriver)

	models, err := driver.ListModels(t.Context())
	require.NoError(t, err)
	assert.NotNil(t, models)
}

func runCanceledContext(
	t *testing.T,
	newDriver func() elelem.Driver,
	request elelem.DriverRequest,
) {
	t.Helper()
	driver := requireDriver(t, newDriver)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := driver.Stream(
		ctx,
		cloneRequest(request),
		func(elelem.Delta) error { return nil },
	)
	require.ErrorIs(t, err, context.Canceled)
}

func runInvalidTranscript(
	t *testing.T,
	newDriver func() elelem.Driver,
	options Options,
) {
	t.Helper()
	driver := requireDriver(t, newDriver)
	request := cloneRequest(options.Request)
	request.Messages = []elelem.Message{{
		Role:       elelem.RoleTool,
		ToolCallID: "orphaned-call",
		Content:    elelem.Text("result"),
	}}
	before := networkCalls(options)

	_, err := driver.Stream(t.Context(), request, nil)
	require.ErrorIs(t, err, elelem.ErrInvalidTranscript)
	assert.Equal(t, before, networkCalls(options))
}

func runCapabilityContract(
	t *testing.T,
	newDriver func() elelem.Driver,
	options Options,
) {
	t.Helper()

	driver := requireDriver(t, newDriver)

	for _, model := range options.capabilityModels() {
		modelOptions := options
		modelOptions.Request = cloneRequest(options.Request)
		modelOptions.Request.Model = model

		capabilities := driver.Capabilities(model)

		t.Run(model.ID, func(t *testing.T) {
			for _, tc := range capabilityCases() {
				t.Run(tc.name, func(t *testing.T) {
					runCapabilityCase(
						t,
						newDriver,
						modelOptions,
						capabilities,
						tc,
					)
				})
			}
		})
	}
}

func runCapabilityCase(
	t *testing.T,
	newDriver func() elelem.Driver,
	options Options,
	capabilities elelem.Capabilities,
	tc capabilityCase,
) {
	t.Helper()
	driver := requireDriver(t, newDriver)
	request := cloneRequest(options.Request)
	tc.configure(&request)

	before := networkCalls(options)

	_, err := driver.Stream(
		t.Context(),
		request,
		func(elelem.Delta) error { return nil },
	)
	if tc.supported(capabilities) {
		require.NoError(t, err)

		if options.NetworkCalls != nil {
			assert.Greater(t, networkCalls(options), before)
		}

		return
	}

	require.Error(t, err)
	assert.Equal(t, before, networkCalls(options))
}

func requireDriver(
	t *testing.T,
	newDriver func() elelem.Driver,
) elelem.Driver {
	t.Helper()

	driver := newDriver()
	require.NotNil(t, driver)
	require.NotNil(t, driver.TokenCounter())

	return driver
}

func assertDeltaContract(t *testing.T, deltas []elelem.Delta) {
	t.Helper()
	require.NotEmpty(t, deltas)

	finishSeen := false
	payloadSeen := false

	for _, delta := range deltas {
		if hasPayload(delta) {
			assert.False(t, finishSeen, "payload delta arrived after finish")

			payloadSeen = true
		}

		if delta.FinishReason == elelem.FinishReasonUnset {
			continue
		}

		assert.True(t, isKnownFinishReason(delta.FinishReason))
		assert.False(t, finishSeen, "multiple finish deltas emitted")
		finishSeen = true
	}

	assert.True(
		t,
		payloadSeen,
		"stream emitted no content/tool/reasoning delta",
	)
	assert.True(t, finishSeen, "stream emitted no finish delta")
}

// assertFinishReasonsAgree binds the two channels a driver reports a finish
// reason through. Validating each in isolation — which this suite did — lets a
// driver classify the same turn differently depending on which one the caller
// reads, and a fix applied to one channel looks complete.
//
// Structural on purpose: it binds BOTH drivers and every future one, so the
// invariant survives a fix verified only against the input that motivated it.
func assertFinishReasonsAgree(
	t *testing.T,
	deltas []elelem.Delta,
	usage elelem.Usage,
) {
	t.Helper()

	var streamed elelem.FinishReason

	for _, delta := range deltas {
		if delta.FinishReason != elelem.FinishReasonUnset {
			streamed = delta.FinishReason
		}
	}

	assert.Equal(
		t,
		usage.FinishReason,
		streamed,
		"delta stream and Usage report different finish reasons",
	)
}

func assertUsageContract(t *testing.T, usage elelem.Usage) {
	t.Helper()
	assert.GreaterOrEqual(t, usage.Prompt, int64(0))
	assert.GreaterOrEqual(t, usage.Completion, int64(0))
	assert.GreaterOrEqual(t, usage.Total, int64(0))
	assert.LessOrEqual(t, usage.Reasoning, usage.Completion)
	assert.LessOrEqual(t, usage.CacheRead+usage.CacheWrite, usage.Prompt)

	// CacheWriteLongTTL ⊆ CacheWrite. Model.Cost defends this with a min()
	// before pricing the remainder at the short-TTL rate, so a driver that
	// over-reports it silently under-prices the difference — the subset was
	// asserted nowhere despite the engine relying on it.
	assert.LessOrEqual(t, usage.CacheWriteLongTTL, usage.CacheWrite)

	assert.True(t, isKnownFinishReason(usage.FinishReason))
}

func hasPayload(delta elelem.Delta) bool {
	return delta.Text != "" ||
		delta.Reasoning != "" ||
		delta.ToolCall != nil ||
		len(delta.ProviderReasoning) > 0
}

func capabilityCases() []capabilityCase {
	testCases := responseAndToolCapabilityCases()

	return append(testCases, generationCapabilityCases()...)
}

func responseAndToolCapabilityCases() []capabilityCase {
	testCases := responseCapabilityCases()

	return append(testCases, toolCapabilityCases()...)
}

func responseCapabilityCases() []capabilityCase {
	return []capabilityCase{
		{
			name: "JSON schema response",
			supported: func(caps elelem.Capabilities) bool {
				return caps.SupportsResponseFormatJSONSchema
			},
			configure: func(request *elelem.DriverRequest) {
				request.Params.ResponseFormat = &elelem.ResponseFormat{
					Type:         elelem.ResponseFormatTypeJSONSchema,
					Name:         "conformance_response",
					Schema:       json.RawMessage(`{"type":"object"}`),
					StrictSchema: true,
				}
			},
		},
		{
			name: "JSON object response",
			supported: func(caps elelem.Capabilities) bool {
				return caps.SupportsResponseFormatJSONObject
			},
			configure: func(request *elelem.DriverRequest) {
				request.Params.ResponseFormat = &elelem.ResponseFormat{
					Type: elelem.ResponseFormatTypeJSONObject,
				}
			},
		},
	}
}

func toolCapabilityCases() []capabilityCase {
	return []capabilityCase{
		{
			name: "strict tool arguments",
			supported: func(caps elelem.Capabilities) bool {
				return caps.SupportsStrictToolArguments
			},
			configure: func(request *elelem.DriverRequest) {
				request.Tools = []elelem.Tool{{
					Name:            "lookup",
					ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
					StrictArguments: true,
				}}
			},
		},
		{
			name: "specific tool choice",
			supported: func(caps elelem.Capabilities) bool {
				return caps.SupportsToolChoice
			},
			configure: func(request *elelem.DriverRequest) {
				request.Tools = []elelem.Tool{{
					Name:            "lookup",
					ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
				}}
				request.Params.ToolChoice = elelem.ToolChoiceTool("lookup")
			},
		},
		{
			name: "parallel tool calls",
			supported: func(caps elelem.Capabilities) bool {
				return caps.SupportsParallelToolCalls
			},
			configure: func(request *elelem.DriverRequest) {
				value := false
				request.Tools = []elelem.Tool{{Name: "lookup"}}
				request.Params.ParallelToolCalls = &value
			},
		},
	}
}

func generationCapabilityCases() []capabilityCase {
	return []capabilityCase{
		{
			name: "seed",
			supported: func(caps elelem.Capabilities) bool {
				return caps.SupportsSeed
			},
			configure: func(request *elelem.DriverRequest) {
				value := capabilityTestSeed
				request.Params.Seed = &value
			},
		},
		{
			name: "sampling penalties",
			supported: func(caps elelem.Capabilities) bool {
				return caps.SupportsSamplingPenalties
			},
			configure: func(request *elelem.DriverRequest) {
				value := capabilityTestValue
				request.Params.FrequencyPenalty = &value
			},
		},
		{
			name: "sampling parameters",
			supported: func(caps elelem.Capabilities) bool {
				return caps.SupportsSamplingParams
			},
			configure: func(request *elelem.DriverRequest) {
				value := capabilityTestValue
				request.Params.Temperature = &value
			},
		},
		{
			name: "reasoning effort",
			supported: func(caps elelem.Capabilities) bool {
				return caps.SupportsReasoningEffort
			},
			configure: func(request *elelem.DriverRequest) {
				request.Params.ReasoningEffort = elelem.ReasoningEffortLow
			},
		},
		{
			name: "disable reasoning",
			supported: func(caps elelem.Capabilities) bool {
				return caps.SupportsDisablingReasoning
			},
			configure: func(request *elelem.DriverRequest) {
				request.Params.ReasoningEffort = elelem.ReasoningEffortNone
			},
		},
	}
}

func cloneRequest(request elelem.DriverRequest) elelem.DriverRequest {
	request.Messages = append([]elelem.Message(nil), request.Messages...)
	request.Tools = append([]elelem.Tool(nil), request.Tools...)

	request.Params.Stop = append([]string(nil), request.Params.Stop...)
	if request.Params.ResponseFormat != nil {
		format := *request.Params.ResponseFormat
		format.Schema = append(json.RawMessage(nil), format.Schema...)
		request.Params.ResponseFormat = &format
	}

	return request
}

func networkCalls(options Options) int64 {
	if options.NetworkCalls == nil {
		return 0
	}

	return options.NetworkCalls()
}

func isKnownFinishReason(reason elelem.FinishReason) bool {
	switch reason {
	case elelem.FinishReasonUnset,
		elelem.FinishReasonStop,
		elelem.FinishReasonLength,
		elelem.FinishReasonToolCalls,
		elelem.FinishReasonContentFilter,
		elelem.FinishReasonStopSequence,
		elelem.FinishReasonPaused,
		elelem.FinishReasonContextExceeded,
		elelem.FinishReasonFunctionCall:
		return true
	default:
		return false
	}
}
