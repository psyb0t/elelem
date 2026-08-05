package elelem

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestClient_CapabilitiesAppliesTheOverride(t *testing.T) {
	t.Parallel()

	driverCaps := Capabilities{
		SupportsImageInput: true,
		SupportsAudioInput: true,
	}

	testCases := []struct {
		name     string
		override func(Model, Capabilities) Capabilities
		want     Capabilities
	}{
		{
			"no override reports the driver verbatim",
			nil,
			driverCaps,
		},
		{
			"an override restricts what the driver claims",
			func(_ Model, caps Capabilities) Capabilities {
				caps.SupportsImageInput = false

				return caps
			},
			Capabilities{SupportsAudioInput: true},
		},
		{
			"an override may replace the answer entirely",
			func(Model, Capabilities) Capabilities {
				return Capabilities{SupportsFileInput: true}
			},
			Capabilities{SupportsFileInput: true},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := New(
				&capsDriver{caps: driverCaps},
				WithCapabilityOverride(tc.override),
			)

			assert.Equal(t, tc.want, client.Capabilities(Model{ID: "m"}))
		})
	}
}

// The override takes the Model because capabilities are per-model: one gateway
// can front a vision model and a text-only one, and a fixed struct would
// flatten the two into whichever answer was written down.
func TestClient_CapabilityOverrideSeesTheModel(t *testing.T) {
	t.Parallel()

	client := New(
		&capsDriver{caps: Capabilities{SupportsImageInput: true}},
		WithCapabilityOverride(func(
			model Model,
			caps Capabilities,
		) Capabilities {
			caps.SupportsImageInput = model.ID == "vision"

			return caps
		}),
	)

	assert.True(t, client.Capabilities(Model{ID: "vision"}).SupportsImageInput)
	assert.False(t, client.Capabilities(Model{ID: "text"}).SupportsImageInput)
}

// The override is only worth having if the gate reads through it. A driver
// claiming image support while the gateway behind it serves vision some other
// way must still refuse locally, before the request ships.
func TestClient_CapabilityOverrideGatesTheRequest(t *testing.T) {
	t.Parallel()

	driver := &contentGateDriver{caps: Capabilities{SupportsImageInput: true}}

	client := New(driver, WithCapabilityOverride(func(
		_ Model,
		caps Capabilities,
	) Capabilities {
		caps.SupportsImageInput = false

		return caps
	}))

	_, err := NewRequest(client).
		WithModel(Model{ID: "m", ContextSize: 100_000}).
		WithPrompt(NewPrompt().User(
			TextOf("look"),
			ImageBytes([]byte("png"), MediaTypePNG),
		)).
		Run(t.Context())

	require.ErrorIs(t, err, ErrUnsupportedContent)
	assert.Equal(t, 0, driver.streams,
		"an overridden-away capability must not become a provider request")
}

// streamingTestDriver returns a driver scripted with one plain text turn, so
// every streaming test below differs only in how the client or request was
// configured.
func streamingTestDriver() *scriptedDriver {
	return &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "hi", FinishReason: FinishReasonStop}},
		usage:  Usage{FinishReason: FinishReasonStop},
	}}}
}

func runStreamingTest(t *testing.T, client *Client) {
	t.Helper()

	_, err := NewRequest(client).
		WithModel(Model{ID: "m", ContextSize: 100_000}).
		WithPrompt(NewPrompt().UserText("q")).
		Run(t.Context())
	require.NoError(t, err)
}

// Streaming is what elelem has always done, so an untouched client must keep
// doing it — this is the regression guard on the default, which is the one
// thing a new toggle can silently invert for every existing caller.
func TestWithStreaming_DefaultsToStreaming(t *testing.T) {
	t.Parallel()

	driver := streamingTestDriver()
	runStreamingTest(t, New(driver))

	assert.Equal(t, 1, driver.streamCalls)
	assert.Equal(t, 0, driver.completeCalls)
}

// The client option is the one that matters in practice: "everything through
// this endpoint is queued" is a property of the base URL, not of one request.
func TestWithStreaming_ClientOptionRoutesToComplete(t *testing.T) {
	t.Parallel()

	driver := streamingTestDriver()
	runStreamingTest(t, New(driver, WithStreaming(false)))

	assert.Equal(t, 0, driver.streamCalls)
	assert.Equal(t, 1, driver.completeCalls,
		"streaming off must take Driver.Complete")
}

// Request beats client, in BOTH directions — the asymmetric bug would be a
// request-level true that cannot re-enable streaming a client disabled.
func TestWithStreaming_RequestOverridesClient(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		clientOn      bool
		requestOn     bool
		wantStreams   int
		wantCompletes int
	}{
		{"request off beats client on", true, false, 0, 1},
		{"request on beats client off", false, true, 1, 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			driver := streamingTestDriver()
			client := New(driver, WithStreaming(tc.clientOn))

			_, err := NewRequest(client).
				WithModel(Model{ID: "m", ContextSize: 100_000}).
				WithPrompt(NewPrompt().UserText("q")).
				WithStreaming(tc.requestOn).
				Run(t.Context())
			require.NoError(t, err)

			assert.Equal(t, tc.wantStreams, driver.streamCalls)
			assert.Equal(t, tc.wantCompletes, driver.completeCalls)
		})
	}
}

// A provider that cannot stream overrules the preference rather than erroring:
// there is nothing to choose between.
func TestWithStreaming_CapabilityOverrulesPreference(t *testing.T) {
	t.Parallel()

	driver := streamingTestDriver()
	driver.capabilities = Capabilities{StreamingUnsupported: true}

	client := New(driver, WithStreaming(true))

	_, err := NewRequest(client).
		WithModel(Model{ID: "m", ContextSize: 100_000}).
		WithPrompt(NewPrompt().UserText("q")).
		WithStreaming(true).
		Run(t.Context())
	require.NoError(t, err)

	assert.Equal(t, 0, driver.streamCalls)
	assert.Equal(t, 1, driver.completeCalls,
		"a provider that cannot stream takes Complete even when asked")
}

// The zero value must mean STREAMING. Phrased positively (SupportsStreaming) a
// driver that simply forgot the field would silently move all its traffic onto
// the non-streaming path — working, but a transport change nobody asked for.
func TestWithStreaming_ZeroValueCapabilitiesStillStream(t *testing.T) {
	t.Parallel()

	driver := streamingTestDriver()
	driver.capabilities = Capabilities{}

	runStreamingTest(t, New(driver))

	assert.Equal(t, 1, driver.streamCalls,
		"zero-value Capabilities must not disable streaming")
}

// Both paths must produce the same Response. The whole design rests on the
// driver feeding the SAME delta callback either way, so a caller cannot tell
// which ran except by timing.
func TestWithStreaming_BothPathsProduceTheSameResponse(t *testing.T) {
	t.Parallel()

	run := func(streaming bool) *Response {
		driver := streamingTestDriver()

		response, err := NewRequest(New(driver, WithStreaming(streaming))).
			WithModel(Model{ID: "m", ContextSize: 100_000}).
			WithPrompt(NewPrompt().UserText("q")).
			Run(t.Context())
		require.NoError(t, err)

		return response
	}

	streamed := run(true)
	completed := run(false)

	assert.Equal(t, streamed.Text, completed.Text)
	assert.Equal(t, streamed.FinishReason, completed.FinishReason)
	assert.Equal(t, streamed.Usage.Total, completed.Usage.Total)
}

// Callbacks must fire on the non-streaming path too — that is the entire
// reason Driver.Complete takes the same delta callback instead of returning a
// Message. If it regressed, a non-streaming turn would render nothing.
func TestWithStreaming_CallbacksFireWhenNotStreaming(t *testing.T) {
	t.Parallel()

	driver := streamingTestDriver()

	var text, deltas string

	_, err := NewRequest(New(driver, WithStreaming(false))).
		WithModel(Model{ID: "m", ContextSize: 100_000}).
		WithPrompt(NewPrompt().UserText("q")).
		OnText(func(_ context.Context, delta TextDelta) error {
			text += delta.Text

			return nil
		}).
		OnDelta(func(_ context.Context, delta Delta) error {
			deltas += delta.Text

			return nil
		}).
		Run(t.Context())
	require.NoError(t, err)

	assert.Equal(t, "hi", text, "OnText must fire without streaming")
	assert.Equal(t, "hi", deltas, "OnDelta must fire without streaming")
}
