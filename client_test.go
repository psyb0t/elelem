package elelem

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
