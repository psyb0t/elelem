// Package zaicoding implements Z.ai Coding's Chat Completions dialect.
package zaicoding

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3/option"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/drivers/openai"
)

const (
	// Name identifies this provider-specific driver.
	Name = "zai-coding"

	// DefaultBaseURL is Z.ai Coding's OpenAI-compatible endpoint.
	DefaultBaseURL = "https://api.z.ai/api/coding/paas/v4"

	providerReasoningVersion = 1

	extraThinkingKey        = "thinking"
	extraReasoningEffortKey = "reasoning_effort"
	extraMaxTokensKey       = "max_tokens"
	extraToolStreamKey      = "tool_stream"
	thinkingTypeKey         = "type"
	clearThinkingKey        = "clear_thinking"
	reasoningContentKey     = "reasoning_content"

	// maxStopWords is the most stop words the endpoint accepts; a longer list
	// fails with business code 1214.
	maxStopWords = 4
)

// Z.ai business codes the driver translates into portable errors.
const (
	codePromptTooLong    = "1261"
	codeSensitiveContent = "1301"
)

// Finish reasons Z.ai adds to the OpenAI set. A stream that ends abnormally
// reports its cause only through one of these.
const (
	finishReasonSensitive             = "sensitive"
	finishReasonContextWindowExceeded = "model_context_window_exceeded"
	finishReasonNetworkError          = "network_error"
)

// isQuotaCode reports the 429 business codes that mean a spent balance, an
// exhausted plan window, an expired plan, or a model outside the plan. None
// clears within a retry backoff, unlike 1302 (request rate) and 1305
// (overload).
func isQuotaCode(code string) bool {
	switch code {
	case "1113", "1308", "1309", "1310", "1311", "1313", "1314", "1315",
		"1316", "1317", "1318", "1319", "1320", "1321":
		return true
	default:
		return false
	}
}

type thinkingType string

const thinkingTypeEnabled thinkingType = "enabled"

//nolint:tagliatelle // Z.ai wire field names are snake_case.
type thinkingConfig struct {
	Type          thinkingType `json:"type"`
	ClearThinking *bool        `json:"clear_thinking,omitempty"`
}

//nolint:tagliatelle // Z.ai wire field names are snake_case.
type providerReasoningEnvelope struct {
	Provider  string `json:"provider"`
	Version   int    `json:"version"`
	Model     string `json:"model"`
	Reasoning string `json:"reasoning_content"`
}

// Driver translates Elelem requests to Z.ai Coding's OpenAI-shaped API.
type Driver struct {
	openai *openai.Driver
}

type driverConfig struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	sdkOptions []option.RequestOption
}

// DriverOption configures a Z.ai Coding driver.
type DriverOption func(*driverConfig)

// NewDriver constructs a Z.ai Coding driver. It never reads OPENAI_*
// environment settings because those settings can belong to another upstream.
func NewDriver(opts ...DriverOption) *Driver {
	cfg := driverConfig{baseURL: DefaultBaseURL}

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	options := []openai.DriverOption{
		openai.WithoutEnvironmentDefaults(),
		openai.WithAPIKey(cfg.apiKey),
		openai.WithBaseURL(cfg.baseURL),
		openai.WithAssistantMessageExtra(providerReasoningExtra),
		openai.WithFinishReasonMapper(finishReason),
	}

	if cfg.httpClient != nil {
		options = append(options, openai.WithHTTPClient(cfg.httpClient))
	}

	if len(cfg.sdkOptions) > 0 {
		options = append(options, openai.WithSDKOptions(cfg.sdkOptions...))
	}

	return &Driver{openai: openai.NewDriver(options...)}
}

// WithAPIKey supplies the bearer token used by Z.ai Coding.
func WithAPIKey(apiKey string) DriverOption {
	return func(cfg *driverConfig) {
		cfg.apiKey = apiKey
	}
}

// WithBaseURL overrides Z.ai Coding's endpoint.
func WithBaseURL(baseURL string) DriverOption {
	return func(cfg *driverConfig) {
		if strings.TrimSpace(baseURL) != "" {
			cfg.baseURL = baseURL
		}
	}
}

// WithHTTPClient supplies the HTTP client used by the underlying SDK.
func WithHTTPClient(client *http.Client) DriverOption {
	return func(cfg *driverConfig) {
		cfg.httpClient = client
	}
}

// WithSDKOptions appends official OpenAI SDK options for this driver only.
func WithSDKOptions(opts ...option.RequestOption) DriverOption {
	return func(cfg *driverConfig) {
		cfg.sdkOptions = append(cfg.sdkOptions, opts...)
	}
}

// Stream issues one streaming Z.ai Coding chat completion. When the request
// carries tools it asks for tool_stream, so tool-call arguments arrive in
// fragments instead of in one piece at the end of the call.
func (d *Driver) Stream(
	ctx context.Context,
	req elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	prepared, err := prepareRequest(req, true)
	if err != nil {
		return elelem.Usage{}, ctxerrors.Wrap(
			err,
			"prepare Z.ai Coding stream request",
		)
	}

	capture := newReasoningCapture(prepared.Model.ID, onDelta)

	usage, err := d.openai.Stream(ctx, prepared, capture.handler())
	if err != nil {
		return usage, ctxerrors.Wrap(
			providerError(err),
			"stream Z.ai Coding chat completion",
		)
	}

	if err := capture.flush(); err != nil {
		return usage, err
	}

	return usage, nil
}

// Complete issues one non-streaming Z.ai Coding chat completion.
func (d *Driver) Complete(
	ctx context.Context,
	req elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	prepared, err := prepareRequest(req, false)
	if err != nil {
		return elelem.Usage{}, ctxerrors.Wrap(
			err,
			"prepare Z.ai Coding completion request",
		)
	}

	capture := newReasoningCapture(prepared.Model.ID, onDelta)

	usage, err := d.openai.Complete(ctx, prepared, capture.handler())
	if err != nil {
		return usage, ctxerrors.Wrap(
			providerError(err),
			"complete Z.ai Coding chat completion",
		)
	}

	if err := capture.flush(); err != nil {
		return usage, err
	}

	return usage, nil
}

// ListModels returns model IDs exposed by the configured endpoint.
func (d *Driver) ListModels(ctx context.Context) ([]string, error) {
	models, err := d.openai.ListModels(ctx)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list Z.ai Coding models")
	}

	return models, nil
}

// Capabilities reports the selected Z.ai Coding model's validated controls.
func (d *Driver) Capabilities(model elelem.Model) elelem.Capabilities {
	return capabilities(model, d.openai.Capabilities(model))
}

// TokenCounter returns the transport's token counter.
func (d *Driver) TokenCounter() elelem.TokenCounter {
	return d.openai.TokenCounter()
}

func prepareRequest(
	req elelem.DriverRequest,
	streaming bool,
) (elelem.DriverRequest, error) {
	kind := classifyModel(req.Model.ID)

	if err := validateParams(kind, req.Model.ID, req.Params); err != nil {
		return elelem.DriverRequest{}, err
	}

	thinking, reasoningEffort, err := reasoningConfiguration(
		kind,
		req.Model.ID,
		req.Params,
	)
	if err != nil {
		return elelem.DriverRequest{}, err
	}

	params := req.Params

	params.Extra = maps.Clone(params.Extra)
	if params.Extra == nil {
		params.Extra = make(map[string]any)
	}

	// The endpoint caps output only through max_tokens and ignores the
	// max_completion_tokens field the OpenAI transport would send.
	if params.MaxOutputTokens != nil {
		params.Extra[extraMaxTokensKey] = *params.MaxOutputTokens
		params.MaxOutputTokens = nil
	}

	if _, set := params.Extra[extraToolStreamKey]; streaming && !set &&
		len(req.Tools) > 0 {
		params.Extra[extraToolStreamKey] = true
	}

	if thinking != nil {
		params.ReasoningEffort = elelem.ReasoningEffortUnset
		params.Extra[extraThinkingKey] = *thinking
		delete(params.Extra, extraReasoningEffortKey)

		if reasoningEffort != elelem.ReasoningEffortUnset {
			params.Extra[extraReasoningEffortKey] = reasoningEffort
		}
	}

	req.Params = params

	return req, nil
}

// validateParams rejects the controls the endpoint refuses or ignores, so the
// caller gets a local error instead of a provider 400 or a silently dropped
// setting.
func validateParams(
	kind modelKind,
	modelID string,
	params elelem.GenerationParams,
) error {
	if len(params.Stop) > maxStopWords {
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"%d stop words on Z.ai Coding, at most %d",
			len(params.Stop),
			maxStopWords,
		)
	}

	switch params.ToolChoice.Mode {
	case elelem.ToolChoiceModeRequired, elelem.ToolChoiceModeTool:
		// The endpoint accepts these values and then behaves as auto, so a
		// caller that forced a tool would wait for a call that never comes.
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"tool choice %q on Z.ai Coding, which only honors auto and none",
			params.ToolChoice.Mode,
		)
	}

	if kind != modelKindUnknown && params.MaxOutputTokens != nil &&
		*params.MaxOutputTokens > glm53MaxOutputTokens {
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"max output tokens %d on Z.ai Coding model %q, at most %d",
			*params.MaxOutputTokens,
			modelID,
			glm53MaxOutputTokens,
		)
	}

	return nil
}

func reasoningConfiguration(
	kind modelKind,
	modelID string,
	params elelem.GenerationParams,
) (*thinkingConfig, elelem.ReasoningEffort, error) {
	effort := params.ReasoningEffort
	if kind == modelKindUnknown {
		return unknownModelReasoningConfiguration(modelID, effort)
	}

	thinking, err := requestedThinking(params.Extra)
	if err != nil {
		return nil, elelem.ReasoningEffortUnset, err
	}

	if effort == elelem.ReasoningEffortUnset {
		return thinking, elelem.ReasoningEffortUnset, nil
	}

	wireEffort, err := codingPlanReasoningEffort(modelID, effort)
	if err != nil {
		return nil, elelem.ReasoningEffortUnset, err
	}

	return thinking, wireEffort, nil
}

func unknownModelReasoningConfiguration(
	modelID string,
	effort elelem.ReasoningEffort,
) (*thinkingConfig, elelem.ReasoningEffort, error) {
	if effort == elelem.ReasoningEffortUnset {
		return nil, elelem.ReasoningEffortUnset, nil
	}

	return nil, elelem.ReasoningEffortUnset, ctxerrors.Wrapf(
		ErrUnsupportedParameter,
		"reasoning control on unknown Z.ai Coding model %q",
		modelID,
	)
}

// requestedThinking builds the thinking object. Thinking is always enabled on
// GLM 5.3 and GLM 5.3 Flash. clear_thinking defaults to false, which keeps
// earlier turns' reasoning in the conversation, and a caller may set it to
// true through Extra["thinking"].
func requestedThinking(extra map[string]any) (*thinkingConfig, error) {
	clearThinking := false
	thinking := &thinkingConfig{
		Type:          thinkingTypeEnabled,
		ClearThinking: &clearThinking,
	}

	raw, ok := extra[extraThinkingKey]
	if !ok {
		return thinking, nil
	}

	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, ctxerrors.Wrap(
			ErrUnsupportedParameter,
			"Z.ai Coding thinking must be a map with type and clear_thinking",
		)
	}

	if value, set := fields[thinkingTypeKey]; set &&
		value != string(thinkingTypeEnabled) {
		return nil, ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"thinking type %v on Z.ai Coding, which only accepts enabled",
			value,
		)
	}

	if value, set := fields[clearThinkingKey]; set {
		requested, isBool := value.(bool)
		if !isBool {
			return nil, ctxerrors.Wrap(
				ErrUnsupportedParameter,
				"Z.ai Coding clear_thinking must be a boolean",
			)
		}

		thinking.ClearThinking = &requested
	}

	return thinking, nil
}

// codingPlanReasoningEffort applies the Coding Plan's documented effort
// mapping for GLM 5.3 and GLM 5.3 Flash: minimal and low become low, medium
// and high become high, xhigh and max become max. Thinking cannot be turned
// off, so none is refused rather than silently sent as low.
func codingPlanReasoningEffort(
	modelID string,
	effort elelem.ReasoningEffort,
) (elelem.ReasoningEffort, error) {
	switch effort {
	case elelem.ReasoningEffortMinimal, elelem.ReasoningEffortLow:
		return elelem.ReasoningEffortLow, nil
	case elelem.ReasoningEffortMedium, elelem.ReasoningEffortHigh:
		return elelem.ReasoningEffortHigh, nil
	case elelem.ReasoningEffortXHigh, elelem.ReasoningEffortMax:
		return elelem.ReasoningEffortMax, nil
	default:
		return elelem.ReasoningEffortUnset, unsupportedReasoningEffort(
			modelID,
			effort,
		)
	}
}

func unsupportedReasoningEffort(
	modelID string,
	effort elelem.ReasoningEffort,
) error {
	return ctxerrors.Wrapf(
		ErrUnsupportedParameter,
		"reasoning effort %q on Z.ai Coding model %q",
		effort,
		modelID,
	)
}

// finishReason maps Z.ai's own finish reasons. network_error means the
// generation died in transit, which Z.ai's error table reports as code 1234
// with HTTP 500 when it arrives as an error instead, so it becomes the same
// retryable server failure.
func finishReason(raw string) (elelem.FinishReason, error) {
	switch raw {
	case finishReasonSensitive:
		return elelem.FinishReasonContentFilter, nil
	case finishReasonContextWindowExceeded:
		return elelem.FinishReasonContextExceeded, nil
	case finishReasonNetworkError:
		return elelem.FinishReasonUnset, &elelem.ProviderError{
			Cause: ctxerrors.Wrap(
				commerr.ErrUnavailable,
				"Z.ai Coding ended the response with network_error",
			),
			StatusCode: http.StatusInternalServerError,
			Code:       finishReasonNetworkError,
		}
	default:
		return elelem.FinishReasonUnset, nil
	}
}

// providerError translates Z.ai business codes into portable errors. A quota,
// plan, or balance failure is rebuilt without the rate-limit sentinel the
// transport joined for its 429 status, so nothing reads it as retryable.
func providerError(err error) error {
	providerErr, ok := errors.AsType[*elelem.ProviderError](err)
	if !ok {
		return err
	}

	code := providerErr.ErrorCode()
	if isQuotaCode(code) {
		return &elelem.ProviderError{
			Cause: ctxerrors.Wrapf(
				elelem.ErrQuotaExhausted,
				"Z.ai Coding code %s: %s",
				code,
				providerErr.Error(),
			),
			StatusCode:      providerErr.HTTPStatus(),
			RetryAfterDelay: providerErr.RetryAfterDelay,
			Code:            code,
		}
	}

	switch code {
	case codePromptTooLong:
		return errors.Join(err, elelem.ErrContextExceeded)
	case codeSensitiveContent:
		return errors.Join(err, ErrSensitiveContent)
	default:
		return err
	}
}

// reasoningCapture forwards deltas and accumulates the response's reasoning,
// then publishes the replay envelope once: just before the finish delta, which
// must stay the last payload, or after the response when no finish delta
// arrived. Encoding it on every reasoning delta cost time quadratic in the
// reasoning length on long max-effort turns.
type reasoningCapture struct {
	modelID   string
	onDelta   func(elelem.Delta) error
	reasoning strings.Builder
	published bool
}

func newReasoningCapture(
	modelID string,
	onDelta func(elelem.Delta) error,
) *reasoningCapture {
	return &reasoningCapture{modelID: modelID, onDelta: onDelta}
}

func (c *reasoningCapture) handler() func(elelem.Delta) error {
	if c.onDelta == nil {
		return nil
	}

	return func(delta elelem.Delta) error {
		c.reasoning.WriteString(delta.Reasoning)

		if delta.FinishReason != elelem.FinishReasonUnset {
			if err := c.flush(); err != nil {
				return err
			}
		}

		if err := c.onDelta(delta); err != nil {
			return ctxerrors.Wrap(err, "publish Z.ai Coding delta")
		}

		return nil
	}
}

func (c *reasoningCapture) flush() error {
	if c.onDelta == nil || c.published || c.reasoning.Len() == 0 {
		return nil
	}

	c.published = true

	payload, err := json.Marshal(providerReasoningEnvelope{
		Provider:  Name,
		Version:   providerReasoningVersion,
		Model:     c.modelID,
		Reasoning: c.reasoning.String(),
	})
	if err != nil {
		return ctxerrors.Wrap(err, "encode Z.ai Coding reasoning")
	}

	if err := c.onDelta(elelem.Delta{ProviderReasoning: payload}); err != nil {
		return ctxerrors.Wrap(err, "publish Z.ai Coding reasoning")
	}

	return nil
}

func providerReasoningExtra(
	ctx context.Context,
	model elelem.Model,
	message elelem.Message,
) (map[string]any, error) {
	if len(message.ProviderReasoning) == 0 {
		return map[string]any{}, nil
	}

	var envelope providerReasoningEnvelope
	if err := json.Unmarshal(message.ProviderReasoning, &envelope); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"discarding undecodable provider reasoning",
			"reason", elelem.LogReasonProviderReasoningUndecodable,
			"model", model.ID,
			"err", err,
		)

		return map[string]any{}, nil
	}

	if envelope.Provider != Name ||
		envelope.Version != providerReasoningVersion ||
		envelope.Model != model.ID ||
		envelope.Reasoning == "" {
		ctxscope.GetLogger(ctx).Warn(
			"discarding mismatched provider reasoning",
			"reason", elelem.LogReasonProviderReasoningMismatch,
			"model", model.ID,
		)

		return map[string]any{}, nil
	}

	return map[string]any{reasoningContentKey: envelope.Reasoning}, nil
}

var _ elelem.Driver = (*Driver)(nil)
