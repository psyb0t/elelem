// Package zaicoding implements Z.ai Coding's Chat Completions dialect.
package zaicoding

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3/option"
	"github.com/psyb0t/ctxerrors"
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
	reasoningContentKey     = "reasoning_content"
)

type thinkingType string

const (
	thinkingTypeEnabled  thinkingType = "enabled"
	thinkingTypeDisabled thinkingType = "disabled"
)

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

// Stream issues one streaming Z.ai Coding chat completion.
func (d *Driver) Stream(
	ctx context.Context,
	req elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	prepared, err := prepareRequest(req)
	if err != nil {
		return elelem.Usage{}, ctxerrors.Wrap(
			err,
			"prepare Z.ai Coding stream request",
		)
	}

	usage, err := d.openai.Stream(
		ctx,
		prepared,
		withProviderReasoning(prepared.Model.ID, onDelta),
	)
	if err != nil {
		return usage, ctxerrors.Wrap(err, "stream Z.ai Coding chat completion")
	}

	return usage, nil
}

// Complete issues one non-streaming Z.ai Coding chat completion.
func (d *Driver) Complete(
	ctx context.Context,
	req elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	prepared, err := prepareRequest(req)
	if err != nil {
		return elelem.Usage{}, ctxerrors.Wrap(
			err,
			"prepare Z.ai Coding completion request",
		)
	}

	usage, err := d.openai.Complete(
		ctx,
		prepared,
		withProviderReasoning(prepared.Model.ID, onDelta),
	)
	if err != nil {
		return usage, ctxerrors.Wrap(
			err,
			"complete Z.ai Coding chat completion",
		)
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

func prepareRequest(req elelem.DriverRequest) (elelem.DriverRequest, error) {
	thinking, reasoningEffort, err := reasoningConfiguration(
		req.Model.ID,
		req.Params.ReasoningEffort,
	)
	if err != nil {
		return elelem.DriverRequest{}, err
	}

	if thinking == nil {
		return req, nil
	}

	params := req.Params
	params.ReasoningEffort = elelem.ReasoningEffortUnset

	params.Extra = maps.Clone(params.Extra)
	if params.Extra == nil {
		params.Extra = make(map[string]any)
	}

	params.Extra[extraThinkingKey] = *thinking
	delete(params.Extra, extraReasoningEffortKey)

	if reasoningEffort != elelem.ReasoningEffortUnset {
		params.Extra[extraReasoningEffortKey] = reasoningEffort
	}

	req.Params = params

	return req, nil
}

func reasoningConfiguration(
	modelID string,
	effort elelem.ReasoningEffort,
) (*thinkingConfig, elelem.ReasoningEffort, error) {
	kind := classifyModel(modelID)
	if kind == modelKindUnknown {
		return unknownModelReasoningConfiguration(modelID, effort)
	}

	if effort == elelem.ReasoningEffortNone {
		return disabledThinkingConfiguration(kind, modelID)
	}

	thinking := enabledThinking()
	if effort == elelem.ReasoningEffortUnset {
		return thinking, elelem.ReasoningEffortUnset, nil
	}

	wireEffort, err := mappedReasoningEffort(kind, modelID, effort)
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

func disabledThinkingConfiguration(
	kind modelKind,
	modelID string,
) (*thinkingConfig, elelem.ReasoningEffort, error) {
	switch kind {
	case modelKindGLM53, modelKindGLM53Flash, modelKindUnknown:
		return nil, elelem.ReasoningEffortUnset, unsupportedReasoningEffort(
			modelID,
			elelem.ReasoningEffortNone,
		)
	}

	return nil, elelem.ReasoningEffortUnset, unsupportedReasoningEffort(
		modelID,
		elelem.ReasoningEffortNone,
	)
}

func mappedReasoningEffort(
	kind modelKind,
	modelID string,
	effort elelem.ReasoningEffort,
) (elelem.ReasoningEffort, error) {
	switch kind {
	case modelKindGLM53:
		return glm53ReasoningEffort(modelID, effort)
	case modelKindUnknown, modelKindGLM53Flash:
		return elelem.ReasoningEffortUnset, unsupportedReasoningEffort(
			modelID,
			effort,
		)
	}

	return elelem.ReasoningEffortUnset, unsupportedReasoningEffort(
		modelID,
		effort,
	)
}

func glm53ReasoningEffort(
	modelID string,
	effort elelem.ReasoningEffort,
) (elelem.ReasoningEffort, error) {
	switch effort {
	case elelem.ReasoningEffortLow,
		elelem.ReasoningEffortHigh,
		elelem.ReasoningEffortMax:
		return effort, nil
	default:
		return elelem.ReasoningEffortUnset, unsupportedReasoningEffort(
			modelID,
			effort,
		)
	}
}

func enabledThinking() *thinkingConfig {
	clearThinking := false

	return &thinkingConfig{
		Type:          thinkingTypeEnabled,
		ClearThinking: &clearThinking,
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

func withProviderReasoning(
	modelID string,
	onDelta func(elelem.Delta) error,
) func(elelem.Delta) error {
	if onDelta == nil {
		return nil
	}

	var reasoning strings.Builder

	return func(delta elelem.Delta) error {
		if delta.Reasoning != "" {
			reasoning.WriteString(delta.Reasoning)

			payload, err := json.Marshal(providerReasoningEnvelope{
				Provider:  Name,
				Version:   providerReasoningVersion,
				Model:     modelID,
				Reasoning: reasoning.String(),
			})
			if err != nil {
				return ctxerrors.Wrap(err, "encode Z.ai Coding reasoning")
			}

			delta.ProviderReasoning = payload
		}

		if err := onDelta(delta); err != nil {
			return ctxerrors.Wrap(err, "publish Z.ai Coding delta")
		}

		return nil
	}
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
