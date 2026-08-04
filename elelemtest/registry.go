package elelemtest

import "sync"

// The global registry exists because the Driver is built deep inside the app's
// own wiring (upstream registry -> driver construction), so a test that wants
// to program it never holds the reference. The alternative — handing tests a
// constructor hook — means the test exercises a wiring path production never
// takes, which is the divergence real-infra testing exists to prevent.
//
// Production code reaches this only under `testing.Testing()`; see the driver
// selection in internal/pkg/services/http-server.
var registry struct { //nolint:gochecknoglobals // test-double registry
	mutex  sync.RWMutex
	driver *ScriptedDriver
}

// SetGlobalScriptedDriver installs the driver the app's wiring will hand out
// while running under `go test`. Call ResetGlobalScriptedDriver in a cleanup;
// leaving one installed leaks a script into the next test.
func SetGlobalScriptedDriver(driver *ScriptedDriver) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	registry.driver = driver
}

// GlobalScriptedDriver returns the installed driver, or nil when none is set.
func GlobalScriptedDriver() *ScriptedDriver {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()

	return registry.driver
}

// ResetGlobalScriptedDriver clears the registry.
func ResetGlobalScriptedDriver() {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	registry.driver = nil
}
