package mcp

import "errors"

var (
	// ErrInvalidServer marks a Server that cannot be connected as given.
	ErrInvalidServer = errors.New("invalid MCP server")
	// ErrConnectFailed marks a server that could not be started or reached,
	// or whose tools could not be listed.
	ErrConnectFailed = errors.New("MCP server connection failed")
	// ErrServerStopped marks a call to a server whose connection ended.
	ErrServerStopped = errors.New("MCP server stopped")
	// ErrNotSupported marks a call for a feature the server did not
	// advertise, such as ListPrompts on a server without prompts. The server
	// is not asked.
	ErrNotSupported = errors.New("MCP server does not offer this feature")
	// ErrResultTooLarge marks a resource read or a prompt larger than the
	// limit the Server sets for it.
	ErrResultTooLarge = errors.New("MCP result is too large")
	// ErrElicitationRefused marks a call the server would only run after a
	// URL elicitation the Elicit handler did not accept.
	ErrElicitationRefused = errors.New("MCP elicitation was not accepted")
)
