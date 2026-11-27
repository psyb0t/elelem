package mcp

import (
	"context"
	"fmt"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Progress is one progress notification a server sent for a tool call.
// Message is server-written, untrusted text.
type Progress struct {
	// Progress grows with every notification. Total is the value it ends
	// at, or zero when the server does not know.
	Progress float64
	Total    float64
	Message  string
}

type progressKey struct{}

// WithProgress returns a context that makes an MCP tool call it reaches
// ask its server for progress notifications and pass each one to report.
// report runs on the connection's notification goroutine, so it must
// return quickly. It can run up to a second after the call returns, for
// notifications the server sent just before its result. A server that
// sends no progress never calls it.
func WithProgress(
	ctx context.Context,
	report func(Progress),
) context.Context {
	return context.WithValue(ctx, progressKey{}, report)
}

func progressReporter(ctx context.Context) func(Progress) {
	report, _ := ctx.Value(progressKey{}).(func(Progress))

	return report
}

// watchProgress routes notifications for a new token to report until
// progressLinger after release is called. The SDK queues notifications
// while it hands the result straight to the call, so the last ones the
// server sent before its result can be handled after the call returns.
func (c *Connection) watchProgress(
	report func(Progress),
) (string, func()) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.nextToken++
	token := fmt.Sprintf(progressIDFormat, c.nextToken)
	c.progress[token] = report

	return token, func() {
		time.AfterFunc(progressLinger, func() {
			c.mutex.Lock()
			delete(c.progress, token)
			c.mutex.Unlock()
		})
	}
}

func (c *Connection) progressNotified(
	_ context.Context,
	request *sdk.ProgressNotificationClientRequest,
) {
	if request.Params == nil {
		return
	}

	token, isString := request.Params.ProgressToken.(string)
	if !isString {
		return
	}

	c.mutex.Lock()
	report := c.progress[token]
	c.mutex.Unlock()

	if report == nil {
		return
	}

	report(Progress{
		Progress: request.Params.Progress,
		Total:    request.Params.Total,
		Message:  request.Params.Message,
	})
}
