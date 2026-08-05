package elelem

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const structuredConcurrentCalls = 16

var (
	errNoStructuredTurn           = errors.New("no structured test turn")
	errUnexpectedConcurrentTarget = errors.New("unexpected concurrent target")
)

type structuredResult struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type structuredTestTurn struct {
	text  string
	usage Usage
	err   error
}

type structuredTestDriver struct {
	mutex        sync.Mutex
	turns        []structuredTestTurn
	requests     []DriverRequest
	capabilities Capabilities
}

func (d *structuredTestDriver) Complete(
	ctx context.Context,
	request DriverRequest,
	onDelta func(Delta) error,
) (Usage, error) {
	return d.Stream(ctx, request, onDelta)
}

func (d *structuredTestDriver) Stream(
	_ context.Context,
	request DriverRequest,
	onDelta func(Delta) error,
) (Usage, error) {
	d.mutex.Lock()

	d.requests = append(d.requests, request)
	if len(d.turns) == 0 {
		d.mutex.Unlock()

		return Usage{}, errNoStructuredTurn
	}

	turn := d.turns[0]
	d.turns = d.turns[1:]
	d.mutex.Unlock()

	if onDelta != nil && turn.text != "" {
		if err := onDelta(Delta{Text: turn.text}); err != nil {
			return turn.usage, err
		}
	}

	return turn.usage, turn.err
}

func (d *structuredTestDriver) ListModels(context.Context) ([]string, error) {
	return []string{"structured-model"}, nil
}

func (d *structuredTestDriver) Capabilities(Model) Capabilities {
	return d.capabilities
}

func (d *structuredTestDriver) TokenCounter() TokenCounter {
	return fixedCounter(0)
}

func (d *structuredTestDriver) Requests() []DriverRequest {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	requests := make([]DriverRequest, len(d.requests))
	for index := range d.requests {
		requests[index] = d.requests[index]
		requests[index].Messages = cloneMessages(d.requests[index].Messages)
		requests[index].Params = cloneParams(d.requests[index].Params)
	}

	return requests
}

func newStructuredRequest(
	driver *structuredTestDriver,
	model Model,
) *Request {
	if model.ID == "" {
		model.ID = "structured-model"
	}

	client := New(driver, WithDefaultModel(model))

	return NewRequest(client).WithPrompt(NewPrompt().UserText("analyze"))
}

func successfulStructuredUsage(prompt, completion int64) Usage {
	return Usage{
		TokenCounts: TokenCounts{
			Prompt:     prompt,
			Completion: completion,
			Total:      prompt + completion,
		},
		Model:        "structured-model",
		FinishReason: FinishReasonStop,
	}
}

func TestRunInto_DerivesStrictSchemaWithoutMutatingRequest(t *testing.T) {
	t.Parallel()

	driver := &structuredTestDriver{
		turns: []structuredTestTurn{{
			text:  `{"label":"ready","count":2}`,
			usage: successfulStructuredUsage(3, 4),
		}},
		capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
	}
	request := newStructuredRequest(driver, Model{})
	target := structuredResult{Label: "unchanged", Count: -1}

	response, err := request.RunInto(t.Context(), &target)
	require.NoError(t, err)
	assert.Equal(t, structuredResult{Label: "ready", Count: 2}, target)
	assert.Equal(t, int64(7), response.Usage.Total)
	assert.Nil(t, request.params.ResponseFormat)

	requests := driver.Requests()
	require.Len(t, requests, 1)
	format := requests[0].Params.ResponseFormat
	require.NotNil(t, format)
	assert.Equal(t, ResponseFormatTypeJSONSchema, format.Type)
	assert.Equal(t, structuredResponseSchemaName, format.Name)
	assert.True(t, format.StrictSchema)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(format.Schema, &schema))
	assert.Equal(t, "object", schema["type"])
	assert.ElementsMatch(t, []any{"label", "count"}, schema["required"])
	assert.Equal(t, false, schema["additionalProperties"])
}

// A typed object and a tool list are competing answers to the same turn: hand
// the model both and it can satisfy the request by calling the tool, leaving
// nothing to decode.
//
// The suppression used to be implicit — the structured turn was dispatched with
// Complete, whose withTools: false dropped them at the engine. Run infers the
// tool loop from configuration instead, so a request that carries tools would
// now send them here unless cloneForStructuredResponse clears them. Deleting
// those two lines leaves every other test in the suite green.
func TestRunInto_DoesNotSendToolsConfiguredOnTheRequest(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		arrange func(*Request) *Request
	}{
		{
			name: "static tool",
			arrange: func(request *Request) *Request {
				return request.WithTool(Tool{Name: "lookup"})
			},
		},
		{
			name: "tool provider",
			arrange: func(request *Request) *Request {
				return request.WithToolProvider(
					func(context.Context) (*ToolSet, error) {
						return NewToolSet(Tool{Name: "lookup"}), nil
					},
				)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			driver := &structuredTestDriver{
				turns: []structuredTestTurn{{
					text:  `{"label":"ready","count":2}`,
					usage: successfulStructuredUsage(3, 4),
				}},
				capabilities: Capabilities{
					SupportsResponseFormatJSONSchema: true,
				},
			}

			request := tc.arrange(newStructuredRequest(driver, Model{}))

			var target structuredResult

			_, err := request.RunInto(t.Context(), &target)
			require.NoError(t, err)
			assert.Equal(
				t,
				structuredResult{Label: "ready", Count: 2},
				target,
			)

			requests := driver.Requests()
			require.Len(t, requests, 1)
			assert.Empty(t, requests[0].Tools,
				"a structured turn must not offer the model a tool to call")
		})
	}
}

func TestRunInto_RejectsInvalidTargetBeforeCallingDriver(t *testing.T) {
	t.Parallel()

	driver := &structuredTestDriver{
		capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
	}
	request := newStructuredRequest(driver, Model{})

	var nilTarget *structuredResult

	testCases := []struct {
		name   string
		target any
	}{
		{name: "nil", target: nil},
		{name: "non pointer", target: structuredResult{}},
		{name: "nil pointer", target: nilTarget},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := request.RunInto(t.Context(), tc.target)
			require.ErrorIs(t, err, ErrInvalidRequest)
		})
	}

	assert.Empty(t, driver.Requests())
}

func TestRunInto_TruncationIsDistinctAndNeverRepairs(t *testing.T) {
	t.Parallel()

	driver := &structuredTestDriver{
		turns: []structuredTestTurn{{
			text:  `{"label":"cut`,
			usage: Usage{FinishReason: FinishReasonLength},
		}},
		capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
	}
	request := newStructuredRequest(driver, Model{}).WithResponseRepair()
	target := structuredResult{Label: "preserved", Count: 9}

	response, err := request.RunInto(t.Context(), &target)
	require.ErrorIs(t, err, ErrResponseTruncated)
	require.NotNil(t, response)
	assert.Equal(t, structuredResult{Label: "preserved", Count: 9}, target)
	assert.Len(t, driver.Requests(), 1)
}

// A refusal is as unrepairable as a truncation. Re-asking buys a SECOND BILLED
// round-trip and the same refusal, then reports it to the operator as a schema
// mismatch that never existed.
//
// Asserting on driver.Requests() rather than on the predicate: the
// classification was plumbed correctly for five review rounds while this
// consumer never read it, so the cost the fix exists to prevent was still
// being paid. Counting the calls is the only assertion that catches that.
func TestRunInto_RefusalIsDistinctAndNeverRepairs(t *testing.T) {
	t.Parallel()

	driver := &structuredTestDriver{
		turns: []structuredTestTurn{
			{
				text:  "I cannot help with that.",
				usage: Usage{FinishReason: FinishReasonContentFilter},
			},
			// A second turn is scripted deliberately: if the repair round
			// fires it succeeds and the test would otherwise pass on the
			// assigned value, hiding the extra billed call.
			{
				text:  `{"label":"repaired","count":1}`,
				usage: successfulStructuredUsage(1, 1),
			},
		},
		capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
	}
	request := newStructuredRequest(driver, Model{}).WithResponseRepair()
	target := structuredResult{Label: "preserved", Count: 9}

	response, err := request.RunInto(t.Context(), &target)
	require.Error(t, err)
	require.NotNil(t, response)

	assert.Len(t, driver.Requests(), 1,
		"a refused turn must not be re-asked")
	assert.Equal(t, structuredResult{Label: "preserved", Count: 9}, target,
		"a refusal must not assign a repaired value")
	assert.Equal(t, FinishReasonContentFilter, response.FinishReason)
}

func TestRunInto_DoesNotAssignMalformedResponse(t *testing.T) {
	t.Parallel()

	driver := &structuredTestDriver{
		turns: []structuredTestTurn{{
			text:  `{"label":"private-value"`,
			usage: successfulStructuredUsage(1, 1),
		}},
		capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
	}
	target := structuredResult{Label: "preserved", Count: 9}

	request := newStructuredRequest(driver, Model{})
	_, err := request.RunInto(t.Context(), &target)
	require.ErrorIs(t, err, ErrResponseSchemaMismatch)
	assert.NotContains(t, err.Error(), "private-value")
	assert.Equal(t, structuredResult{Label: "preserved", Count: 9}, target)
}

func TestRunInto_StrictValidationRejectsMissingField(t *testing.T) {
	t.Parallel()

	driver := &structuredTestDriver{
		turns: []structuredTestTurn{{
			text:  `{"label":"partial"}`,
			usage: successfulStructuredUsage(1, 1),
		}},
		capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
	}
	target := structuredResult{Label: "preserved", Count: 9}

	_, err := newStructuredRequest(driver, Model{}).
		WithStrictResponseValidation().
		RunInto(t.Context(), &target)
	require.ErrorIs(t, err, ErrResponseSchemaMismatch)
	assert.NotContains(t, err.Error(), "partial")
	assert.Equal(t, structuredResult{Label: "preserved", Count: 9}, target)
}

func TestRunInto_DefaultValidationAllowsMissingField(t *testing.T) {
	t.Parallel()

	driver := &structuredTestDriver{
		turns: []structuredTestTurn{{
			text:  `{"label":"partial"}`,
			usage: successfulStructuredUsage(1, 1),
		}},
		capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
	}
	target := structuredResult{Count: 9}

	_, err := newStructuredRequest(driver, Model{}).
		RunInto(t.Context(), &target)
	require.NoError(t, err)
	assert.Equal(t, structuredResult{Label: "partial"}, target)
}

func TestRunInto_RepairsOnceAndAccumulatesAccounting(t *testing.T) {
	t.Parallel()

	driver := &structuredTestDriver{
		turns: []structuredTestTurn{
			{
				text:  `{"label":"private-value"}`,
				usage: successfulStructuredUsage(1, 2),
			},
			{
				text:  `{"label":"repaired","count":3}`,
				usage: successfulStructuredUsage(4, 5),
			},
		},
		capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
	}
	model := Model{
		ID:      "structured-model",
		Pricing: ModelPricing{InputPerToken: 1, OutputPerToken: 2},
	}
	target := structuredResult{Label: "preserved", Count: 9}

	response, err := newStructuredRequest(driver, model).
		WithStrictResponseValidation().
		WithResponseRepair().
		RunInto(t.Context(), &target)
	require.NoError(t, err)
	assert.Equal(t, structuredResult{Label: "repaired", Count: 3}, target)
	assert.Equal(t, int64(5), response.Usage.Prompt)
	assert.Equal(t, int64(7), response.Usage.Completion)
	assert.Equal(t, int64(12), response.Usage.Total)
	assert.Equal(t, float64(19), response.Cost)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	require.GreaterOrEqual(t, len(requests[1].Messages), 3)
	repairMessage := requests[1].Messages[len(requests[1].Messages)-1]
	assert.Equal(t, RoleUser, repairMessage.Role)
	// The repair turn must carry BOTH the instruction and the reason the
	// previous reply was rejected — a repair prompt that never says what broke
	// is a re-roll, not a repair.
	assert.Contains(t, repairMessage.Text(), repairResponsePrompt)
	assert.Contains(
		t,
		repairMessage.Text(),
		"failed validation",
		"repair turn feeds the validation error back",
	)
	assert.Contains(
		t,
		repairMessage.Text(),
		"schema validation",
		"repair turn names the specific failure",
	)
	assert.NotContains(t, repairMessage.Text(), "private-value")
	assert.Equal(
		t,
		requests[0].Params.ResponseFormat,
		requests[1].Params.ResponseFormat,
	)
}

func TestRunInto_ResponseRepairIsBoundedToOneFollowUp(t *testing.T) {
	t.Parallel()

	driver := &structuredTestDriver{
		turns: []structuredTestTurn{
			{text: `{}`, usage: successfulStructuredUsage(1, 1)},
			{text: `{}`, usage: successfulStructuredUsage(2, 2)},
		},
		capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
	}
	target := structuredResult{Label: "preserved", Count: 9}

	response, err := newStructuredRequest(driver, Model{}).
		WithStrictResponseValidation().
		WithResponseRepair().
		RunInto(t.Context(), &target)
	require.ErrorIs(t, err, ErrResponseSchemaMismatch)
	assert.Equal(t, structuredResult{Label: "preserved", Count: 9}, target)
	assert.Equal(t, int64(6), response.Usage.Total)
	assert.Len(t, driver.Requests(), 2)
}

func TestRunInto_RepairFailurePreservesAllAccounting(t *testing.T) {
	t.Parallel()

	driver := &structuredTestDriver{
		turns: []structuredTestTurn{
			{
				text:  `{}`,
				usage: successfulStructuredUsage(1, 2),
			},
			{
				text:  `{"label":"partial"}`,
				usage: successfulStructuredUsage(4, 5),
				err:   assert.AnError,
			},
		},
		capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
	}
	target := structuredResult{Label: "preserved", Count: 9}

	response, err := newStructuredRequest(driver, Model{}).
		WithStrictResponseValidation().
		WithResponseRepair().
		RunInto(t.Context(), &target)
	require.ErrorIs(t, err, assert.AnError)
	require.NotNil(t, response)
	assert.Equal(t, int64(5), response.Usage.Prompt)
	assert.Equal(t, int64(7), response.Usage.Completion)
	assert.Equal(t, int64(12), response.Usage.Total)
	assert.Equal(t, structuredResult{Label: "preserved", Count: 9}, target)
	assert.Len(t, driver.Requests(), 2)
}

func TestRunInto_SharedRequestIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	turns := make([]structuredTestTurn, structuredConcurrentCalls)
	for index := range turns {
		turns[index] = structuredTestTurn{
			text:  `{"label":"parallel","count":1}`,
			usage: successfulStructuredUsage(1, 1),
		}
	}

	driver := &structuredTestDriver{
		turns:        turns,
		capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
	}
	request := newStructuredRequest(driver, Model{})

	var wait sync.WaitGroup

	errorsByCall := make(chan error, structuredConcurrentCalls)
	for range structuredConcurrentCalls {
		wait.Go(func() {
			target := structuredResult{}

			_, err := request.RunInto(t.Context(), &target)

			expected := structuredResult{Label: "parallel", Count: 1}
			if err == nil && target != expected {
				err = errUnexpectedConcurrentTarget
			}

			errorsByCall <- err
		})
	}

	wait.Wait()
	close(errorsByCall)

	for err := range errorsByCall {
		require.NoError(t, err)
	}

	assert.Nil(t, request.params.ResponseFormat)
	assert.Len(t, driver.Requests(), structuredConcurrentCalls)
}

func TestRunInto_RejectsUnsupportedSchemaType(t *testing.T) {
	t.Parallel()

	type invalidTarget struct {
		Handler func() `json:"handler"`
	}

	driver := &structuredTestDriver{
		capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
	}
	target := invalidTarget{}

	_, err := newStructuredRequest(driver, Model{}).
		RunInto(t.Context(), &target)
	require.ErrorIs(t, err, ErrInvalidRequest)
	assert.Empty(t, driver.Requests())
}

func TestRequest_ResponseFormatValidationRejectsLocally(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		format       *ResponseFormat
		capabilities Capabilities
	}{
		{
			name:   "JSON object capability",
			format: &ResponseFormat{Type: ResponseFormatTypeJSONObject},
		},
		{
			name: "JSON schema capability",
			format: &ResponseFormat{
				Type:   ResponseFormatTypeJSONSchema,
				Name:   "response",
				Schema: json.RawMessage(`{"type":"object"}`),
			},
		},
		{
			name: "JSON schema name",
			format: &ResponseFormat{
				Type:   ResponseFormatTypeJSONSchema,
				Schema: json.RawMessage(`{"type":"object"}`),
			},
			capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
		},
		{
			name: "JSON schema syntax",
			format: &ResponseFormat{
				Type:   ResponseFormatTypeJSONSchema,
				Name:   "response",
				Schema: json.RawMessage(`{`),
			},
			capabilities: Capabilities{SupportsResponseFormatJSONSchema: true},
		},
		{
			name:   "unknown response format",
			format: &ResponseFormat{Type: "unknown"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			driver := &structuredTestDriver{capabilities: tc.capabilities}
			request := newStructuredRequest(driver, Model{})
			request.params.ResponseFormat = tc.format

			_, err := request.Run(t.Context())
			require.ErrorIs(t, err, ErrInvalidRequest)
			assert.Empty(t, driver.Requests())
		})
	}
}

func TestRequest_ReasoningAndToolCapabilitiesRejectLocally(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		model        Model
		capabilities Capabilities
		configure    func(*Request)
	}{
		{
			name:  "reasoning unsupported",
			model: Model{ID: "model", SupportsReasoning: true},
			configure: func(request *Request) {
				request.WithReasoningEffort(ReasoningEffortHigh)
			},
		},
		{
			name:         "reasoning effort invalid",
			model:        Model{ID: "model", SupportsReasoning: true},
			capabilities: Capabilities{SupportsReasoningEffort: true},
			configure: func(request *Request) {
				request.WithReasoningEffort("invalid")
			},
		},
		{
			name:  "reasoning effort above maximum",
			model: Model{ID: "model", SupportsReasoning: true},
			capabilities: Capabilities{
				SupportsReasoningEffort: true,
				MaxReasoningEffort:      ReasoningEffortLow,
			},
			configure: func(request *Request) {
				request.WithReasoningEffort(ReasoningEffortHigh)
			},
		},
		{
			name:  "tool choice unsupported",
			model: Model{ID: "model"},
			configure: func(request *Request) {
				request.
					WithTool(Tool{Name: "lookup"}).
					WithToolChoiceMode(ToolChoiceModeRequired)
			},
		},
		{
			name:         "tool choice mode invalid",
			model:        Model{ID: "model"},
			capabilities: Capabilities{SupportsToolChoice: true},
			configure: func(request *Request) {
				request.
					WithTool(Tool{Name: "lookup"}).
					WithToolChoiceMode("invalid")
			},
		},
		{
			name:         "strict tool arguments unsupported",
			model:        Model{ID: "model"},
			capabilities: Capabilities{SupportsToolChoice: true},
			configure: func(request *Request) {
				request.WithTool(Tool{
					Name:            "lookup",
					ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
					StrictArguments: true,
				})
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			driver := &structuredTestDriver{
				capabilities: tc.capabilities,
			}
			request := newStructuredRequest(driver, tc.model)
			tc.configure(request)

			_, err := request.Run(t.Context())
			require.ErrorIs(t, err, ErrInvalidRequest)
			assert.Empty(t, driver.Requests())
		})
	}
}
