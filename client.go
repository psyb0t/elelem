package elelem

type clientConfig struct {
	defaultModel Model
	tokenCounter TokenCounter
}

// Option configures process-level defaults on a Client. Anything that varies
// per call belongs on Request instead.
type Option func(*clientConfig)

// Client is a Driver plus process-level defaults. It carries no
// per-conversation state, so ONE client serves every request and is safe for
// concurrent use; Request is where an individual call is shaped.
type Client struct {
	driver Driver
	config clientConfig
}

// New builds a Client over the given Driver. A nil Option is skipped, so
// conditional wiring needs no branch at the call site.
func New(driver Driver, opts ...Option) *Client {
	config := clientConfig{}

	for _, opt := range opts {
		if opt != nil {
			opt(&config)
		}
	}

	return &Client{driver: driver, config: config}
}

// WithDefaultModel supplies the Model used when a Request sets none. Without
// it a model-less request falls back to a bare Model with no metadata, which
// silently disables the context-size checks.
func WithDefaultModel(model Model) Option {
	return func(config *clientConfig) {
		config.defaultModel = model
	}
}

// WithClientTokenCounter sets the counter for every request off this client.
// It sits between the per-request override and the driver's own estimator in
// the resolution order: request → client → driver → package default → built-in.
func WithClientTokenCounter(counter TokenCounter) Option {
	return func(config *clientConfig) {
		config.tokenCounter = counter
	}
}

// Driver returns the underlying driver so callers can compose their own
// decorators around it. Returns nil for a nil Client rather than panicking —
// this is a read-only accessor and a nil check at every call site is noise.
//

func (c *Client) Driver() Driver {
	if c == nil {
		return nil
	}

	return c.driver
}
