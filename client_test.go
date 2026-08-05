package elelem

import (
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
		Complete(t.Context())

	require.ErrorIs(t, err, ErrUnsupportedContent)
	assert.Equal(t, 0, driver.streams,
		"an overridden-away capability must not become a provider request")
}
