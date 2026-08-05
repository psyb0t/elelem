//go:build sdkprobe

// Package sdkprobe drives the vendored provider SDKs DIRECTLY — no elelem
// types, no engine, no drivers — to answer with evidence rather than
// inference: does the non-streaming call actually work against the compat
// backends we point at, and what comes back?
//
// It lives in its own package and behind its own build tag so it compiles
// against the SDKs alone, and so a normal `go test ./...` never sees it.
//
//	go test -tags sdkprobe -v ./sdkprobe/
//
// Every test self-skips when its endpoint env vars are absent, so this can
// never turn a credential-less run red.
package sdkprobe

import (
	"context"
	"os"
	"testing"
	"time"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	openaisdk "github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
)

const probeTimeout = 90 * time.Second

// TestProbeAnthropicNonStreamingTokenGuard needs no credentials and no
// network: CalculateNonStreamingTimeout runs before the request is built. It
// decides whether elelem can treat non-streaming as a plain toggle on this
// provider, or has to gate it on max_tokens.
func TestProbeAnthropicNonStreamingTokenGuard(t *testing.T) {
	model := os.Getenv("PROBE_ANTHROPIC_MODEL")
	if model == "" {
		model = "claude-sonnet-4-5"
	}

	for _, maxTokens := range []int{1024, 8192, 21333, 21334, 32000, 64000} {
		_, err := anthropicsdk.CalculateNonStreamingTimeout(
			maxTokens,
			model,
			nil,
		)
		t.Logf("model=%s max_tokens=%-6d allowed=%-5t err=%v",
			model, maxTokens, err == nil, err)
	}
}

// TestProbeOpenAICompatNonStreaming asks the OpenAI-shaped endpoint for a
// non-streaming completion. Chat.Completions.New omits the `stream` field
// entirely — NewStreaming is the one that sets it — so this is the exact wire
// shape elelem's Complete path would produce.
func TestProbeOpenAICompatNonStreaming(t *testing.T) {
	baseURL := os.Getenv("PROBE_OPENAI_BASE_URL")
	apiKey := os.Getenv("PROBE_OPENAI_API_KEY")
	model := os.Getenv("PROBE_OPENAI_MODEL")

	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("PROBE_OPENAI_* not set")
	}

	client := openaisdk.NewClient(
		openaioption.WithBaseURL(baseURL),
		openaioption.WithAPIKey(apiKey),
	)

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	completion, err := client.Chat.Completions.New(
		ctx,
		openaisdk.ChatCompletionNewParams{
			Model: model,
			Messages: []openaisdk.ChatCompletionMessageParamUnion{
				openaisdk.UserMessage("Reply with exactly: OK"),
			},
			MaxTokens: openaisdk.Int(2048),
		},
	)
	if err != nil {
		t.Fatalf("non-streaming call failed: %v", err)
	}

	if len(completion.Choices) == 0 {
		t.Fatalf("no choices returned; raw: %s", completion.RawJSON())
	}

	choice := completion.Choices[0]
	t.Logf("finish_reason=%q content=%q tool_calls=%d refusal=%q",
		choice.FinishReason,
		choice.Message.Content,
		len(choice.Message.ToolCalls),
		choice.Message.Refusal,
	)
	t.Logf("usage prompt=%d completion=%d",
		completion.Usage.PromptTokens, completion.Usage.CompletionTokens)
}

// TestProbeAnthropicCompatNonStreaming does the same against the
// Anthropic-shaped endpoint, reporting the content-block shape the driver's
// Complete path has to translate into deltas.
func TestProbeAnthropicCompatNonStreaming(t *testing.T) {
	baseURL := os.Getenv("PROBE_ANTHROPIC_BASE_URL")
	authToken := os.Getenv("PROBE_ANTHROPIC_AUTH_TOKEN")
	model := os.Getenv("PROBE_ANTHROPIC_MODEL")

	if baseURL == "" || authToken == "" || model == "" {
		t.Skip("PROBE_ANTHROPIC_* not set")
	}

	client := anthropicsdk.NewClient(
		anthropicoption.WithBaseURL(baseURL),
		anthropicoption.WithAuthToken(authToken),
	)

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	message, err := client.Messages.New(ctx, anthropicsdk.MessageNewParams{
		Model:     model,
		MaxTokens: 32,
		Messages: []anthropicsdk.MessageParam{
			anthropicsdk.NewUserMessage(
				anthropicsdk.NewTextBlock("Reply with exactly: OK"),
			),
		},
	})
	if err != nil {
		t.Fatalf("non-streaming call failed: %v", err)
	}

	t.Logf("stop_reason=%q blocks=%d", message.StopReason, len(message.Content))

	for i, block := range message.Content {
		t.Logf("  block[%d] type=%q text=%q", i, block.Type, block.Text)
	}

	t.Logf("usage input=%d output=%d",
		message.Usage.InputTokens, message.Usage.OutputTokens)
}
