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
)
