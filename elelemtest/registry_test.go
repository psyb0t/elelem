package elelemtest

import (
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The registry is process-wide state, so these tests are deliberately NOT
// parallel and each one clears it on cleanup. A leaked driver would hand the
// next test someone else's script -- the exact failure the reset exists to
// prevent.
func TestGlobalScriptedDriver_InstallAndReset(t *testing.T) {
	t.Cleanup(ResetGlobalScriptedDriver)

	require.Nil(
		t,
		GlobalScriptedDriver(),
		"registry must start empty; a previous test leaked a driver",
	)

	driver := NewScriptedDriver()
	SetGlobalScriptedDriver(driver)
	assert.Same(t, driver, GlobalScriptedDriver())

	ResetGlobalScriptedDriver()
	assert.Nil(t, GlobalScriptedDriver())
}

func TestGlobalScriptedDriver_LastInstallWins(t *testing.T) {
	t.Cleanup(ResetGlobalScriptedDriver)

	first := NewScriptedDriver()
	second := NewScriptedDriver()

	SetGlobalScriptedDriver(first)
	SetGlobalScriptedDriver(second)

	assert.Same(t, second, GlobalScriptedDriver())
}

// The scripted driver stands in for a real one, so what it REPORTS about
// itself has to be programmable too: a test covering a capability gate needs
// the double to deny the capability, and one covering a budget needs it to
// count. Both were settable and neither was pinned.
func TestScriptedDriver_ReportsProgrammedCapabilitiesAndCounter(t *testing.T) {
	t.Parallel()

	const countPerMessage = 7

	model := elelem.Model{ID: "scripted"}

	t.Run("capabilities default to permissive", func(t *testing.T) {
		t.Parallel()

		capabilities := NewScriptedDriver().Capabilities(model)
		assert.True(t, capabilities.SupportsToolChoice)
	})

	t.Run("WithCapabilities replaces them wholesale", func(t *testing.T) {
		t.Parallel()

		driver := NewScriptedDriver().
			WithCapabilities(elelem.Capabilities{SupportsSeed: true})

		capabilities := driver.Capabilities(model)
		assert.True(t, capabilities.SupportsSeed)
		assert.False(
			t,
			capabilities.SupportsToolChoice,
			"replacing capabilities must not leave defaults behind",
		)
	})

	t.Run("WithTokenCounter installs the counter", func(t *testing.T) {
		t.Parallel()

		driver := NewScriptedDriver().
			WithTokenCounter(countingCounter(countPerMessage))

		counter := driver.TokenCounter()
		require.NotNil(t, counter)

		count, err := counter.Count([]elelem.Message{{}, {}}, nil)
		require.NoError(t, err)
		assert.Equal(t, countPerMessage*2, count)
	})
}

type countingCounter int

func (c countingCounter) Count(
	messages []elelem.Message,
	_ []elelem.Tool,
) (int, error) {
	return int(c) * len(messages), nil
}
