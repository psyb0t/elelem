// Package openai adapts the official OpenAI Go SDK to elelem.Driver.
// OpenAI-compatible endpoints are supported through WithBaseURL.
package openai

import (
	"context"
	"errors"
	"net/http"
	"sort"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
)

const sdkRetryAttempts = 0

// Name is the provider identifier for this driver — usable by callers
// selecting a driver by name. OpenAI-compatible endpoints reuse it.
const Name = "openai"

// Driver implements elelem.Driver over the official OpenAI Go SDK.
type Driver struct {
	api openaisdk.Client
}

type driverConfig struct {
	options []option.RequestOption
}

// DriverOption configures an OpenAI driver.
type DriverOption func(*driverConfig)

// NewDriver builds an OpenAI-compatible driver.
func NewDriver(opts ...DriverOption) *Driver {
	cfg := driverConfig{options: []option.RequestOption{
		option.WithMaxRetries(sdkRetryAttempts),
	}}

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	return &Driver{api: openaisdk.NewClient(cfg.options...)}
}

// WithAPIKey supplies the bearer token used by the upstream. When omitted, the
// SDK reads OPENAI_API_KEY.
func WithAPIKey(key string) DriverOption {
	return func(cfg *driverConfig) {
		if key == "" {
			return
		}

		cfg.options = append(cfg.options, option.WithAPIKey(key))
	}
}

// WithBaseURL points the driver at an OpenAI-compatible endpoint.
func WithBaseURL(baseURL string) DriverOption {
	return func(cfg *driverConfig) {
		if baseURL == "" {
			return
		}

		// Credentials stripped BEFORE the SDK sees them: the SDK embeds the
		// request URL in the text of every error it builds, and this driver
		// logs those errors, so a userinfo password would reach the log on the
		// first failure. These SDKs authenticate by API-key header and ignore
		// userinfo, so nothing that worked is lost.
		sanitized, _ := elelem.SanitizeBaseURL(baseURL)

		cfg.options = append(cfg.options, option.WithBaseURL(sanitized))
	}
}

// WithHTTPClient supplies the HTTP client used for upstream requests.
func WithHTTPClient(client *http.Client) DriverOption {
	return func(cfg *driverConfig) {
		cfg.options = append(cfg.options, option.WithHTTPClient(client))
	}
}

// WithSDKOptions applies official SDK request options at driver construction.
func WithSDKOptions(opts ...option.RequestOption) DriverOption {
	return func(cfg *driverConfig) {
		cfg.options = append(cfg.options, opts...)
	}
}

// Stream issues one streaming chat completion.
func (d *Driver) Stream(
	ctx context.Context,
	req elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	var usage elelem.Usage

	if err := validateTranscript(req.Messages); err != nil {
		return usage, err
	}

	params, err := toOpenAIParams(req)
	if err != nil {
		return usage, err
	}

	logger := ctxscope.GetLogger(ctx)

	// The provider call is this package's only external dependency; without a
	// line on each side of it a hung or failed upstream leaves no trace.
	logger.Debug(
		"openai request starting",
		"model", req.Model.ID,
		"messages", len(req.Messages),
		"tools", len(req.Tools),
	)

	stream := d.api.Chat.Completions.NewStreaming(
		ctx,
		params,
		extraOptions(req.Params.Extra)...,
	)

	rawFinishReason, streamErr := consumeStream(stream, onDelta, &usage)
	if streamErr != nil {
		logger.Warn(
			"openai stream failed",
			"reason", elelem.LogReasonStreamReadFailed,
			"model", req.Model.ID,
			"err", streamErr,
		)

		return usage, streamErr
	}

	warnUnmappedFinishReason(ctx, req.Model.ID, rawFinishReason, usage)

	logger.Debug(
		"openai request completed",
		"model", usage.Model,
		"finish_reason", usage.FinishReason,
		"prompt_tokens", usage.Prompt,
		"completion_tokens", usage.Completion,
	)

	return usage, streamErr
}

// Complete issues the same chat completion with streaming off.
//
// Chat.Completions.New omits the `stream` field entirely rather than sending
// `stream: false` — NewStreaming is the one that appends
// option.WithJSONSet("stream", true) — which is what a strict compat backend
// needs, since such a backend can reject an unexpected `stream: false` as
// readily as `stream: true`.
//
// The response is adapted into ONE ChatCompletionChunk and pushed through the
// exact same publishChunk path the stream uses. That is deliberate: the
// refusal promotion, the tool-call index narrowing and the finish-reason
// mapping all live in that path and are each load-bearing. A second
// translation here would be the obvious way to write this and the reliable way
// to make the two paths disagree.
func (d *Driver) Complete(
	ctx context.Context,
	req elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	var usage elelem.Usage

	if err := validateTranscript(req.Messages); err != nil {
		return usage, err
	}

	params, err := toOpenAIParams(req)
	if err != nil {
		return usage, err
	}

	logger := ctxscope.GetLogger(ctx)

	logger.Debug(
		"openai request starting",
		"model", req.Model.ID,
		"messages", len(req.Messages),
		"tools", len(req.Tools),
		"streaming", false,
	)

	completion, err := d.api.Chat.Completions.New(
		ctx,
		params,
		extraOptions(req.Params.Extra)...,
	)
	if err != nil {
		logger.Warn(
			"openai non-streaming request failed",
			"reason", elelem.LogReasonStreamReadFailed,
			"model", req.Model.ID,
			"err", err,
		)

		return usage, ctxerrors.Wrap(
			normalizeProviderError(err),
			"complete chat completions",
		)
	}

	rawFinishReason, err := publishCompletion(*completion, onDelta, &usage)
	if err != nil {
		return usage, err
	}

	warnUnmappedFinishReason(ctx, req.Model.ID, rawFinishReason, usage)

	logger.Debug(
		"openai request completed",
		"model", usage.Model,
		"finish_reason", usage.FinishReason,
		"prompt_tokens", usage.Prompt,
		"completion_tokens", usage.Completion,
	)

	return usage, nil
}

// publishCompletion turns a finished response into deltas and accumulates its
// usage, returning the provider's raw finish reason for the unmapped-value
// warning.
//
// Reasoning goes first, then everything else via the SAME chunk path the
// stream uses — scanChoiceSignals for the stream-scoped signals, publishChunk
// for the deltas — so the refusal promotion, the tool-call index guard and the
// finish-reason mapping have exactly one implementation between the two modes.
func publishCompletion(
	completion openaisdk.ChatCompletion,
	onDelta func(elelem.Delta) error,
	usage *elelem.Usage,
) (string, error) {
	if err := publishCompletionReasoning(completion, onDelta); err != nil {
		return "", err
	}

	chunk := chunkFromCompletion(completion)

	var (
		rawFinishReason string
		refused         bool
	)

	scanChoiceSignals(chunk, &rawFinishReason, &refused)

	if err := publishChunk(chunk, onDelta, usage, refused); err != nil {
		return rawFinishReason, err
	}

	return rawFinishReason, nil
}

// publishCompletionReasoning emits the non-streaming response's visible
// reasoning, ahead of everything else so the delta order matches the streaming
// path's (deltasFromChunk emits reasoning first).
//
// It reads the RAW body rather than a typed field because `reasoning` and
// `reasoning_content` are compat-backend extensions the SDK does not model —
// which is also why this cannot live inside chunkFromCompletion: a chunk built
// in Go has no raw body to read.
func publishCompletionReasoning(
	completion openaisdk.ChatCompletion,
	onDelta func(elelem.Delta) error,
) error {
	if onDelta == nil || len(completion.Choices) == 0 {
		return nil
	}

	reasoning := reasoningFromRawJSON(completion.Choices[0].Message.RawJSON())
	if reasoning == "" {
		return nil
	}

	if err := onDelta(elelem.Delta{Reasoning: reasoning}); err != nil {
		return ctxerrors.Wrap(err, "publish completion delta")
	}

	return nil
}

// warnUnmappedFinishReason surfaces a provider stop reason no case matched.
// Unknown values normalize to Unset rather than Stop so they can never
// masquerade as a clean finish — but Unset is itself invisible, so this is how
// a NEW provider value gets noticed instead of quietly degrading every caller's
// switch.
func warnUnmappedFinishReason(
	ctx context.Context,
	modelID string,
	raw string,
	usage elelem.Usage,
) {
	if raw == "" || usage.FinishReason != elelem.FinishReasonUnset {
		return
	}

	ctxscope.GetLogger(ctx).Warn(
		"unmapped provider stop reason",
		"reason", elelem.LogReasonFinishReasonUnmapped,
		"model", modelID,
		"stop_reason", raw,
	)
}

// consumeStream drains the SSE stream and always closes it. The Close error is
// joined rather than discarded — a leaked response body only surfaces under
// sustained load, long after the change that caused it.
func consumeStream(
	stream *ssestream.Stream[openaisdk.ChatCompletionChunk],
	onDelta func(elelem.Delta) error,
	usage *elelem.Usage,
) (string, error) {
	var (
		streamErr       error
		rawFinishReason string
		refused         bool
	)

	// Refusal and finish_reason arrive on DIFFERENT chunks — the terminating
	// chunk carries an empty delta (see testdata/stream.sse). So the promotion
	// has to be stream-scoped: tracked here across chunks, exactly like
	// rawFinishReason, rather than decided per chunk where the two fields are
	// never both present.
	for stream.Next() {
		chunk := stream.Current()
		scanChoiceSignals(chunk, &rawFinishReason, &refused)

		streamErr = publishChunk(chunk, onDelta, usage, refused)
		if streamErr != nil {
			break
		}
	}

	if streamErr == nil {
		if err := stream.Err(); err != nil {
			streamErr = ctxerrors.Wrap(
				normalizeProviderError(err),
				"stream chat completions",
			)
		}
	}

	if closeErr := stream.Close(); closeErr != nil {
		wrappedCloseErr := ctxerrors.Wrap(
			closeErr,
			"close chat completion stream",
		)
		streamErr = errors.Join(streamErr, wrappedCloseErr)
	}

	return rawFinishReason, streamErr
}

// scanChoiceSignals accumulates the stream-scoped signals that no single chunk
// carries on its own.
func scanChoiceSignals(
	chunk openaisdk.ChatCompletionChunk,
	rawFinishReason *string,
	refused *bool,
) {
	if len(chunk.Choices) == 0 {
		return
	}

	choice := chunk.Choices[0]
	if choice.FinishReason != "" {
		*rawFinishReason = choice.FinishReason
	}

	if choice.Delta.Refusal != "" {
		*refused = true
	}
}

func publishChunk(
	event openaisdk.ChatCompletionChunk,
	onDelta func(elelem.Delta) error,
	usage *elelem.Usage,
	refused bool,
) error {
	*usage = usageFromChunk(*usage, event, refused)

	if onDelta == nil {
		return nil
	}

	for _, delta := range deltasFromChunk(event, refused) {
		if err := onDelta(delta); err != nil {
			return ctxerrors.Wrap(err, "publish completion delta")
		}
	}

	return nil
}

// ListModels returns the IDs exposed by the upstream models endpoint.
func (d *Driver) ListModels(ctx context.Context) ([]string, error) {
	ids := make([]string, 0)

	iter := d.api.Models.ListAutoPaging(ctx)
	for iter.Next() {
		ids = append(ids, iter.Current().ID)
	}

	if err := iter.Err(); err != nil {
		return nil, ctxerrors.Wrap(normalizeProviderError(err), "list models")
	}

	return ids, nil
}

// Capabilities reports OpenAI chat-completion capabilities for a model.
func (d *Driver) Capabilities(model elelem.Model) elelem.Capabilities {
	return capabilities(model)
}

// reservedRequestFields are body fields this driver builds itself. Extra may
// not replace them — see extraOptions.
//
//nolint:gochecknoglobals // a set literal; Go has no const map.
var reservedRequestFields = map[string]bool{
	"messages":    true,
	"model":       true,
	"stream":      true,
	"tools":       true,
	"tool_choice": true,
}

// TokenCounter uses elelem's provider-neutral estimator.
func (d *Driver) TokenCounter() elelem.TokenCounter {
	return elelem.DefaultTokenCounter()
}

func extraOptions(extra map[string]any) []option.RequestOption {
	if len(extra) == 0 {
		return nil
	}

	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	opts := make([]option.RequestOption, 0, len(keys))

	for _, key := range keys {
		// Extra sets TOP-LEVEL body fields, so a key naming something the
		// driver already translated silently overwrites it — "messages",
		// "model" or "stream" would replace the transcript, the model, or the
		// streaming mode with no error and no log. Extra is caller-supplied
		// rather than provider-supplied, so this is a footgun rather than an
		// attack, but a footgun that discards a carefully built request is
		// worth refusing.
		if reservedRequestFields[key] {
			continue
		}

		opts = append(opts, option.WithJSONSet(key, extra[key]))
	}

	return opts
}

var _ elelem.Driver = (*Driver)(nil)
