package anthropic

import (
	"context"
	"errors"
	"net/http"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
)

// Name is the provider identifier for this driver — reported on reasoning
// envelopes and usable by callers selecting a driver by name.
const Name = "anthropic"

// defaultMaxOutputTokens is used when the caller sets no output limit. The
// Messages API REQUIRES max_tokens, so unlike OpenAI — which omits the field
// and lets the model decide — this driver must pick something.
//
// It is a real cap, not a formality: against a large-context model a long
// answer stops at this many tokens and the caller sees FinishReasonLength with
// nothing indicating the LIBRARY chose the limit. Set WithMaxOutputTokens
// explicitly for long-form output.
const defaultMaxOutputTokens int64 = 4096

type driverConfig struct {
	sdkOptions []option.RequestOption
}

// Driver translates elelem requests to Anthropic's Messages API.
type Driver struct {
	client anthropicsdk.Client
}

// DriverOption configures an Anthropic driver.
type DriverOption func(*driverConfig)

// NewDriver constructs a driver using the official Anthropic SDK.
func NewDriver(opts ...DriverOption) *Driver {
	cfg := driverConfig{
		sdkOptions: []option.RequestOption{option.WithMaxRetries(0)},
	}

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	return &Driver{client: anthropicsdk.NewClient(cfg.sdkOptions...)}
}

// WithAPIKey supplies an API key instead of reading ANTHROPIC_API_KEY.
func WithAPIKey(key string) DriverOption {
	return func(cfg *driverConfig) {
		cfg.sdkOptions = append(cfg.sdkOptions, option.WithAPIKey(key))
	}
}

// WithBaseURL overrides the Anthropic API endpoint.
func WithBaseURL(baseURL string) DriverOption {
	return func(cfg *driverConfig) {
		// Credentials stripped BEFORE the SDK sees them — see the OpenAI
		// driver's WithBaseURL for why: the SDK embeds the request URL in
		// every error it builds, and those errors get logged.
		sanitized, _ := elelem.SanitizeBaseURL(baseURL)

		cfg.sdkOptions = append(
			cfg.sdkOptions,
			option.WithBaseURL(sanitized),
		)
	}
}

// WithHTTPClient supplies the HTTP client used by the SDK.
func WithHTTPClient(client *http.Client) DriverOption {
	return func(cfg *driverConfig) {
		cfg.sdkOptions = append(cfg.sdkOptions, option.WithHTTPClient(client))
	}
}

// WithSDKOptions appends official SDK request options.
func WithSDKOptions(opts ...option.RequestOption) DriverOption {
	return func(cfg *driverConfig) {
		cfg.sdkOptions = append(cfg.sdkOptions, opts...)
	}
}

// Stream sends one streaming Messages API request.
func (d *Driver) Stream(
	ctx context.Context,
	req elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	params, err := toMessageParams(ctx, req)
	if err != nil {
		return elelem.Usage{}, ctxerrors.Wrap(
			err,
			"translate Anthropic request",
		)
	}

	logger := ctxscope.GetLogger(ctx)

	// The provider call is this package's only external dependency; without a
	// line on each side of it a hung or failed upstream leaves no trace.
	logger.Debug(
		"anthropic request starting",
		"model", req.Model.ID,
		"messages", len(req.Messages),
		"tools", len(req.Tools),
	)

	stream := d.client.Messages.NewStreaming(ctx, params)

	message, err := consumeStream(stream, onDelta)
	if err != nil {
		logger.Warn(
			"anthropic stream failed",
			"reason", elelem.LogReasonStreamReadFailed,
			"model", req.Model.ID,
			"err", err,
		)

		return closeStream(stream, usageFromMessage(message), err)
	}

	usage, err := finishStream(req.Model.ID, message, onDelta)

	// A stop reason we don't recognize normalizes to Unset rather than Stop, so
	// it can never masquerade as a clean finish — but Unset is also invisible.
	// This line is how a NEW provider stop reason gets noticed instead of
	// quietly degrading every caller's switch.
	if raw := string(message.StopReason); raw != "" &&
		usage.FinishReason == elelem.FinishReasonUnset {
		logger.Warn(
			"unmapped provider stop reason",
			"reason", elelem.LogReasonFinishReasonUnmapped,
			"model", req.Model.ID,
			"stop_reason", raw,
		)
	}

	logger.Debug(
		"anthropic request completed",
		"model", usage.Model,
		"finish_reason", usage.FinishReason,
		"prompt_tokens", usage.Prompt,
		"completion_tokens", usage.Completion,
	)

	return closeStream(stream, usage, err)
}

func closeStream(
	stream *ssestream.Stream[anthropicsdk.MessageStreamEventUnion],
	usage elelem.Usage,
	streamErr error,
) (elelem.Usage, error) {
	if closeErr := stream.Close(); closeErr != nil {
		streamErr = errors.Join(
			streamErr,
			ctxerrors.Wrap(closeErr, "close Anthropic stream"),
		)
	}

	return usage, streamErr
}

// Complete sends the same Messages API request with streaming off.
//
// The SDK's Messages.New omits the `stream` field entirely rather than sending
// `stream: false` — see NewStreaming, which is the one that appends
// option.WithJSONSet("stream", true).
//
// It can also refuse BEFORE any request is made: Messages.New calls
// CalculateNonStreamingTimeout, which errors when max_tokens implies a run
// longer than ten minutes (1hr x max_tokens/128000) or exceeds the model's own
// non-streaming cap. That is a client-side rejection with no HTTP round trip,
// so it is wrapped into elelem's own sentinel rather than passed through as a
// bare SDK string — a caller that asked for non-streaming needs to be able to
// match this and decide, not string-match an error message.
func (d *Driver) Complete(
	ctx context.Context,
	req elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	params, err := toMessageParams(ctx, req)
	if err != nil {
		return elelem.Usage{}, ctxerrors.Wrap(
			err,
			"translate Anthropic request",
		)
	}

	logger := ctxscope.GetLogger(ctx)

	logger.Debug(
		"anthropic request starting",
		"model", req.Model.ID,
		"messages", len(req.Messages),
		"tools", len(req.Tools),
		"streaming", false,
	)

	message, err := d.client.Messages.New(ctx, params)
	if err != nil {
		logger.Warn(
			"anthropic non-streaming request failed",
			"reason", elelem.LogReasonStreamReadFailed,
			"model", req.Model.ID,
			"err", err,
		)

		return elelem.Usage{}, ctxerrors.Wrap(
			normalizeNonStreamingError(err),
			"complete Anthropic message",
		)
	}

	// The content deltas the stream would have emitted piecemeal, emitted from
	// the finished blocks instead. finishStream then contributes the same tail
	// it does for a streamed turn (provider reasoning + finish reason), so both
	// paths deliver an identical delta sequence apart from its granularity.
	if err := emitMessageContent(*message, onDelta); err != nil {
		return usageFromMessage(*message), err
	}

	usage, err := finishStream(req.Model.ID, *message, onDelta)
	if err != nil {
		return usage, err
	}

	logger.Debug(
		"anthropic request completed",
		"model", req.Model.ID,
		"finish_reason", usage.FinishReason,
		"prompt_tokens", usage.Prompt,
		"completion_tokens", usage.Completion,
	)

	return usage, nil
}

func consumeStream(
	stream *ssestream.Stream[anthropicsdk.MessageStreamEventUnion],
	onDelta func(elelem.Delta) error,
) (anthropicsdk.Message, error) {
	var message anthropicsdk.Message

	state := newStreamState()

	for stream.Next() {
		event := stream.Current()
		if err := message.Accumulate(event); err != nil {
			return message, ctxerrors.Wrap(
				err,
				"accumulate Anthropic stream event",
			)
		}

		// SDK workaround (anthropic-sdk-go, observed on the vendored version):
		// Message.Accumulate does NOT carry OutputTokensDetails through from
		// the message_delta event, so it is re-copied here. Without it
		// Usage.Reasoning silently reads zero. Re-check on the next SDK bump —
		// if Accumulate starts populating it, this becomes redundant rather
		// than wrong, so nothing will fail loudly.
		deltaEvent, ok := event.AsAny().(anthropicsdk.MessageDeltaEvent)
		if ok {
			outputDetails := deltaEvent.Usage.OutputTokensDetails
			message.Usage.OutputTokensDetails = outputDetails
		}

		if err := emitEventDelta(state, event, onDelta); err != nil {
			return message, ctxerrors.Wrap(
				err,
				"emit Anthropic stream delta",
			)
		}
	}

	if err := stream.Err(); err != nil {
		return message, ctxerrors.Wrap(
			normalizeProviderError(err),
			"read Anthropic stream",
		)
	}

	return message, nil
}

func finishStream(
	modelID string,
	message anthropicsdk.Message,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	providerReasoning, err := marshalProviderReasoning(modelID, message.Content)
	if err != nil {
		return usageFromMessage(message), ctxerrors.Wrap(
			err,
			"marshal Anthropic reasoning blocks",
		)
	}

	if len(providerReasoning) > 0 && onDelta != nil {
		delta := elelem.Delta{ProviderReasoning: providerReasoning}
		if err := onDelta(delta); err != nil {
			return usageFromMessage(message), ctxerrors.Wrap(
				err,
				"emit Anthropic provider reasoning",
			)
		}
	}

	// ONE computation feeding BOTH channels. This used to normalize the stop
	// reason here and again inside usageFromMessage, then overwrite — the same
	// duplicate-derivation shape that let the OpenAI driver's delta stream and
	// Usage report different reasons for one turn. Identical inputs made it
	// harmless here, which is exactly why it would not have been noticed.
	usage := usageFromMessage(message)

	// Only when there IS one. An unmapped or absent stop reason normalizes to
	// Unset, and emitting it anyway pushed a completely empty Delta at the
	// caller — no text, no tool call, no reason — which the OpenAI driver
	// never does. Callers switch on these; a blank one is noise they have to
	// learn to ignore.
	if onDelta != nil && usage.FinishReason != elelem.FinishReasonUnset {
		delta := elelem.Delta{FinishReason: usage.FinishReason}
		if err := onDelta(delta); err != nil {
			return usage, ctxerrors.Wrap(
				err,
				"emit Anthropic finish reason",
			)
		}
	}

	return usage, nil
}

// ListModels returns every model visible to the configured Anthropic account.
func (d *Driver) ListModels(ctx context.Context) ([]string, error) {
	pager := d.client.Models.ListAutoPaging(ctx, anthropicsdk.ModelListParams{})

	models := make([]string, 0)
	for pager.Next() {
		models = append(models, pager.Current().ID)
	}

	if err := pager.Err(); err != nil {
		return nil, ctxerrors.Wrap(
			normalizeProviderError(err),
			"list Anthropic models",
		)
	}

	return models, nil
}

// TokenCounter uses elelem's provider-neutral estimator.
func (d *Driver) TokenCounter() elelem.TokenCounter {
	return elelem.DefaultTokenCounter()
}

var _ elelem.Driver = (*Driver)(nil)
