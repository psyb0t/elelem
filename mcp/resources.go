package mcp

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
)

// Resource is one resource a server lists. Every field is server-written.
type Resource struct {
	URI         string
	Name        string
	Title       string
	Description string
	MIMEType    string
	// Size is the resource's size in bytes, or zero when the server did not
	// say.
	Size int64
}

// ResourceTemplate describes resources by an RFC 6570 URI template, such
// as "file:///{path}". Fill it in to build a URI for ReadResource, and
// pass its arguments to Complete for suggestions.
type ResourceTemplate struct {
	URITemplate string
	Name        string
	Title       string
	Description string
	MIMEType    string
}

// ResourcePage is one page of a server's resources. NextCursor, when not
// empty, fetches the next page.
type ResourcePage struct {
	Resources  []Resource
	NextCursor string
}

// ResourceTemplatePage is one page of a server's resource templates.
// NextCursor, when not empty, fetches the next page.
type ResourceTemplatePage struct {
	Templates  []ResourceTemplate
	NextCursor string
}

// ResourceContents is one item of a read resource: Text, or Blob when Blob
// is not nil. Blob holds the raw bytes, not base64.
type ResourceContents struct {
	URI      string
	MIMEType string
	Text     string
	Blob     []byte
}

func newResourceContents(contents *sdk.ResourceContents) ResourceContents {
	return ResourceContents{
		URI: contents.URI, MIMEType: contents.MIMEType,
		Text: contents.Text, Blob: contents.Blob,
	}
}

// ListResources lists one page of the server's resources. An empty cursor
// fetches the first page and clears Changed(ListKindResources). A server
// without resources fails with ErrNotSupported.
func (c *Connection) ListResources(
	ctx context.Context,
	cursor string,
) (ResourcePage, error) {
	err := c.ready(c.info.Capabilities.Resources, featureResources)
	if err != nil {
		return ResourcePage{}, err
	}

	var result *sdk.ListResourcesResult

	err = c.listing(ListKindResources, cursor, func() error {
		var err error

		result, err = c.session.ListResources(ctx,
			&sdk.ListResourcesParams{Cursor: cursor})

		return requestError(err, "list MCP resources")
	})
	if err != nil {
		return ResourcePage{}, err
	}

	page := ResourcePage{
		Resources:  make([]Resource, 0, len(result.Resources)),
		NextCursor: result.NextCursor,
	}

	for _, resource := range result.Resources {
		page.Resources = append(page.Resources, Resource{
			URI: resource.URI, Name: resource.Name, Title: resource.Title,
			Description: resource.Description, MIMEType: resource.MIMEType,
			Size: resource.Size,
		})
	}

	return page, nil
}

// ListResourceTemplates lists one page of the server's resource
// templates. An empty cursor fetches the first page and clears
// Changed(ListKindResources). A server without resources fails with
// ErrNotSupported.
func (c *Connection) ListResourceTemplates(
	ctx context.Context,
	cursor string,
) (ResourceTemplatePage, error) {
	err := c.ready(c.info.Capabilities.Resources, featureResources)
	if err != nil {
		return ResourceTemplatePage{}, err
	}

	var result *sdk.ListResourceTemplatesResult

	err = c.listing(ListKindResources, cursor, func() error {
		var err error

		result, err = c.session.ListResourceTemplates(ctx,
			&sdk.ListResourceTemplatesParams{Cursor: cursor})

		return requestError(err, "list MCP resource templates")
	})
	if err != nil {
		return ResourceTemplatePage{}, err
	}

	page := ResourceTemplatePage{
		Templates:  make([]ResourceTemplate, 0, len(result.ResourceTemplates)),
		NextCursor: result.NextCursor,
	}

	for _, template := range result.ResourceTemplates {
		page.Templates = append(page.Templates, ResourceTemplate{
			URITemplate: template.URITemplate, Name: template.Name,
			Title: template.Title, Description: template.Description,
			MIMEType: template.MIMEType,
		})
	}

	return page, nil
}

// ReadResource reads the resource at uri. A resource the server does not
// have fails with commerr.ErrNotFound, one larger than
// Server.MaxResourceBytes with ErrResultTooLarge, and a server without
// resources with ErrNotSupported. ResourceResult turns the contents into a
// tool result.
func (c *Connection) ReadResource(
	ctx context.Context,
	uri string,
) ([]ResourceContents, error) {
	err := c.ready(c.info.Capabilities.Resources, featureResources)
	if err != nil {
		return nil, err
	}

	if uri == "" {
		return nil, ctxerrors.Wrap(elelem.ErrInvalidRequest,
			"the resource URI is empty")
	}

	result, err := c.session.ReadResource(ctx,
		&sdk.ReadResourceParams{URI: uri})
	if err != nil {
		if isNotFound(err) {
			err = errors.Join(commerr.ErrNotFound, err)
		}

		return nil, requestError(err, "read MCP resource")
	}

	maxBytes := limit(c.server.MaxResourceBytes, DefaultMaxResourceBytes)
	size := 0
	contents := make([]ResourceContents, 0, len(result.Contents))

	for _, item := range result.Contents {
		if item == nil {
			continue
		}

		size += len(item.Text) + len(item.Blob)
		if size > maxBytes {
			return nil, ctxerrors.Wrapf(ErrResultTooLarge,
				"resource %s is over %d bytes", uri, maxBytes)
		}

		contents = append(contents, newResourceContents(item))
	}

	return contents, nil
}

// Subscribe asks the server to say when the resource at uri changes.
// Server.OnResourceUpdated receives the news. A server that does not offer
// subscriptions fails with ErrNotSupported.
func (c *Connection) Subscribe(ctx context.Context, uri string) error {
	err := c.ready(c.info.Capabilities.ResourceSubscribe,
		featureSubscriptions)
	if err != nil {
		return err
	}

	if uri == "" {
		return ctxerrors.Wrap(elelem.ErrInvalidRequest,
			"the resource URI is empty")
	}

	err = c.session.Subscribe(ctx, &sdk.SubscribeParams{URI: uri})

	return requestError(err, "subscribe to MCP resource")
}

// Unsubscribe stops the notifications Subscribe asked for. Unsubscribing
// from a resource that has no subscription is not an error on the current
// protocol; an older server may refuse it.
func (c *Connection) Unsubscribe(ctx context.Context, uri string) error {
	err := c.ready(c.info.Capabilities.ResourceSubscribe,
		featureSubscriptions)
	if err != nil {
		return err
	}

	if uri == "" {
		return ctxerrors.Wrap(elelem.ErrInvalidRequest,
			"the resource URI is empty")
	}

	err = c.session.Unsubscribe(ctx, &sdk.UnsubscribeParams{URI: uri})

	return requestError(err, "unsubscribe from MCP resource")
}

// ready fails a call on an ended connection, and one for a feature the
// server did not advertise.
func (c *Connection) ready(supported bool, feature string) error {
	if err := c.Err(); err != nil {
		return err
	}

	if !supported {
		return ctxerrors.Wrapf(ErrNotSupported,
			"server %s offers no %s", c.server.Name, feature)
	}

	return nil
}

// listing runs a list call. A first-page call clears the change flag for
// kind, and a failed one puts it back.
func (c *Connection) listing(
	kind ListKind,
	cursor string,
	call func() error,
) error {
	if cursor != "" {
		return call()
	}

	wasChanged := c.takeChanged(kind)

	if err := call(); err != nil {
		if wasChanged {
			c.markChanged(kind)
		}

		return err
	}

	return nil
}

// requestError wraps an SDK error, and keeps nil nil.
func requestError(err error, action string) error {
	if err == nil {
		return nil
	}

	return ctxerrors.Wrap(err, action)
}

// isNotFound reports whether err is the JSON-RPC error a server returns for
// a missing resource: invalid params on the current protocol, -32002 on
// older ones.
func isNotFound(err error) bool {
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) {
		return false
	}

	return rpcErr.Code == jsonrpc.CodeInvalidParams ||
		rpcErr.Code == legacyResourceNotFoundCode
}
