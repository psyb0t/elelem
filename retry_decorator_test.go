// This file is the external test package on purpose. MockDriver lives in
// elelemtest/mocks, which imports elelem -- an in-package test importing it
// would be an import cycle. It is also the natural home for the mock: these
// tests are about what the decorator PASSES to the driver it wraps, which is
// exactly the question MockDriver answers and ScriptedDriver does not.
package elelem_test

import (
	"context"
	"io"
	"testing"
	"time"

	commonerrors "github.com/psyb0t/common-go/errors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	testMaxAttempts  = 3
	testInitialDelay = time.Millisecond
	testMaxDelay     = 2 * time.Millisecond
)

func testRetryConfig() elelem.RetryConfig {
	return elelem.RetryConfig{
		MaxAttempts:  testMaxAttempts,
		InitialDelay: testInitialDelay,
		MaxDelay:     testMaxDelay,
	}
}

// timeoutError is a net.Error reporting a timeout. The stdlib offers no
// constructible one, and the decorator branches on the interface rather than
// on any concrete type.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// The decorator wraps a Driver, so every method it does NOT retry still has
// to reach the wrapped driver unchanged. A passthrough that quietly answered
// for itself would strip the provider's real model list or token counter with
// nothing failing.
func TestWithRetry_PassesNonStreamCallsThrough(t *testing.T) {
	t.Parallel()

	t.Run("ListModels", func(t *testing.T) {
		t.Parallel()

		want := []string{"model-a", "model-b"}
		driver := mocks.NewMockDriver(t)
		driver.EXPECT().
			ListModels(context.Background()).
			Return(want, nil).
			Once()

		got, err := elelem.WithRetry(driver, testRetryConfig()).
			ListModels(context.Background())
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("ListModels error is wrapped, not swallowed", func(t *testing.T) {
		t.Parallel()

		driver := mocks.NewMockDriver(t)
		driver.EXPECT().
			ListModels(context.Background()).
			Return(nil, commonerrors.ErrNotFound).
			Once()

		got, err := elelem.WithRetry(driver, testRetryConfig()).
			ListModels(context.Background())
		require.ErrorIs(t, err, commonerrors.ErrNotFound)
		assert.Nil(t, got)
	})

	t.Run("Capabilities", func(t *testing.T) {
		t.Parallel()

		model := elelem.Model{ID: "model-a"}
		want := elelem.Capabilities{SupportsToolChoice: true}

		driver := mocks.NewMockDriver(t)
		driver.EXPECT().Capabilities(model).Return(want).Once()

		got := elelem.WithRetry(driver, testRetryConfig()).Capabilities(model)
		assert.Equal(t, want, got)
	})

	t.Run("TokenCounter", func(t *testing.T) {
		t.Parallel()

		want := elelem.DefaultTokenCounter()

		driver := mocks.NewMockDriver(t)
		driver.EXPECT().TokenCounter().Return(want).Once()

		got := elelem.WithRetry(driver, testRetryConfig()).TokenCounter()
		assert.Equal(t, want, got)
	})
}

// A connection that broke before the provider ever rendered a verdict is
// retryable regardless of which shape the failure takes. These are the
// transport classifications: nothing carries an HTTP status, so a decorator
// that only understood statuses would give up on all of them.
func TestWithRetry_RetriesTransportFailures(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
	}{
		{name: "net timeout", err: timeoutError{}},
		{name: "EOF", err: io.EOF},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			attempts := 0
			driver := mocks.NewMockDriver(t)
			driver.EXPECT().
				Stream(
					context.Background(),
					elelem.DriverRequest{},
					// The decorator hands the driver its OWN closure so it can
					// tell whether output already started; it is never the
					// caller's callback, so this cannot match on identity.
					mock.Anything,
				).
				RunAndReturn(func(
					context.Context,
					elelem.DriverRequest,
					func(elelem.Delta) error,
				) (elelem.Usage, error) {
					attempts++

					return elelem.Usage{}, tc.err
				}).
				Times(testMaxAttempts)

			_, err := elelem.WithRetry(driver, testRetryConfig()).
				Stream(context.Background(), elelem.DriverRequest{}, nil)

			require.Error(t, err)
			assert.Equal(
				t,
				testMaxAttempts,
				attempts,
				"transport failure should exhaust every attempt",
			)
		})
	}
}

// The mirror of the case above: a failure the provider DID render a verdict
// on, and which no number of retries can change, must stop the loop. Burning
// the remaining attempts on it just spends quota to arrive at the same error.
func TestWithRetry_DoesNotRetryPermanentFailures(t *testing.T) {
	t.Parallel()

	attempts := 0
	driver := mocks.NewMockDriver(t)
	driver.EXPECT().
		Stream(context.Background(), elelem.DriverRequest{}, mock.Anything).
		RunAndReturn(func(
			context.Context,
			elelem.DriverRequest,
			func(elelem.Delta) error,
		) (elelem.Usage, error) {
			attempts++

			return elelem.Usage{}, commonerrors.ErrInvalidArgument
		}).
		Once()

	_, err := elelem.WithRetry(driver, testRetryConfig()).
		Stream(context.Background(), elelem.DriverRequest{}, nil)

	require.ErrorIs(t, err, commonerrors.ErrInvalidArgument)
	assert.Equal(t, 1, attempts, "a permanent failure must not be retried")
}
