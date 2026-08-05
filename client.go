package elelem

type clientConfig struct {
	defaultModel       Model
	tokenCounter       TokenCounter
	capabilityOverride func(Model, Capabilities) Capabilities
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

// WithCapabilityOverride adjusts what the driver reports it can do.
//
// A driver's Capabilities describe the PROVIDER'S API, because that is all a
// driver can know. Point one at a different backend — `WithBaseURL` aimed at
// an Anthropic-compatible or OpenAI-compatible gateway — and those answers
// stop being true: the wire format still matches, the model behind it may not
// read images at all. Without this the engine would pass content the gateway
// cannot serve and the failure would surface as a confusing upstream error.
//
// A function of the model rather than a fixed struct, because capabilities are
// per-model: a single struct would flatten a gateway that fronts a vision
// model and a text-only one behind the same endpoint.
//
//	elelem.New(driver, elelem.WithCapabilityOverride(
//	    func(_ elelem.Model, caps elelem.Capabilities) elelem.Capabilities {
//	        // this gateway serves vision through MCP, not inline
//	        caps.SupportsImageInput = false
//	        return caps
//	    },
//	))
//
// It can only be trusted to RESTRICT. Turning a flag on does not teach the
// driver a translation it does not have: the driver's own per-value gates
// still run — Anthropic's four-media-type image whitelist, its absent audio
// block — and still refuse. Widening a capability the driver cannot express
// moves the error later, it does not remove it.
func WithCapabilityOverride(
	override func(Model, Capabilities) Capabilities,
) Option {
	return func(config *clientConfig) {
		config.capabilityOverride = override
	}
}

// Capabilities reports what the given model supports, with any override from
// WithCapabilityOverride applied. Everything that gates on capabilities reads
// through here, so an override cannot be honoured in one place and missed in
// another.
func (c *Client) Capabilities(model Model) Capabilities {
	caps := c.driver.Capabilities(model)

	if c.config.capabilityOverride == nil {
		return caps
	}

	return c.config.capabilityOverride(model, caps)
}

// Driver returns the underlying driver so callers can compose their own
// decorators around it. Returns nil for a nil Client rather than panicking —
// this is a read-only accessor and a nil check at every call site is noise.
func (c *Client) Driver() Driver {
	if c == nil {
		return nil
	}

	return c.driver
}
