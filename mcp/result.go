package mcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

// ResourceLink is a resource a tool result points at without carrying it.
// Pass URI to ReadResource to read it. Every field is server-written.
type ResourceLink struct {
	URI         string
	Name        string
	Title       string
	Description string
	MIMEType    string
	// Size is the resource's size in bytes, or zero when the server did not
	// say.
	Size int64
}

// piece is one MCP content part as elelem carries it: text, an image, or
// both, and the link a resource link part named.
type piece struct {
	text  string
	image *elelem.ImageSource
	link  *ResourceLink
}

// convertResult maps a tool result onto elelem's. Text parts join into
// Content and image parts become Images. A resource link becomes a note
// and an entry in Metadata[MetadataResourceLinks]. An embedded resource
// becomes a header line, then its text, or its image in Images; any other
// blob is described by type and size, never shown. A part elelem cannot
// carry becomes a short note, so nothing is dropped without saying so.
func convertResult(result *sdk.CallToolResult) (elelem.ToolResult, error) {
	converted := elelem.ToolResult{IsError: result.IsError}
	parts := make([]string, 0, len(result.Content))
	links := make([]ResourceLink, 0)

	for _, content := range result.Content {
		converted.Images, parts = appendPiece(
			converted.Images, parts, contentPiece(content))

		if link, isLink := content.(*sdk.ResourceLink); isLink {
			links = append(links, newResourceLink(link))
		}
	}

	if len(result.Content) == 0 && result.StructuredContent != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return elelem.ToolResult{}, ctxerrors.Wrap(err,
				"encode structured tool result")
		}

		parts = append(parts, string(encoded))
	}

	converted.Content = strings.Join(parts, resultPartSeparator)

	if len(links) > 0 {
		converted.Metadata = map[string]any{MetadataResourceLinks: links}
	}

	return converted, nil
}

func appendPiece(
	images []elelem.ImageSource,
	parts []string,
	part piece,
) ([]elelem.ImageSource, []string) {
	if part.text != "" {
		parts = append(parts, part.text)
	}

	if part.image != nil {
		images = append(images, *part.image)
	}

	return images, parts
}

// contentPiece is one content part as text, an image, or both.
func contentPiece(content sdk.Content) piece {
	switch part := content.(type) {
	case *sdk.TextContent:
		return piece{text: part.Text}
	case *sdk.ImageContent:
		return piece{image: &elelem.ImageSource{
			Data: part.Data, MediaType: part.MIMEType,
		}}
	case *sdk.AudioContent:
		return piece{text: fmt.Sprintf(audioNoteFormat, part.MIMEType)}
	case *sdk.ResourceLink:
		link := newResourceLink(part)

		return piece{text: resourceLinkNote(link), link: &link}
	case *sdk.EmbeddedResource:
		if part.Resource == nil {
			return piece{text: fmt.Sprintf(unknownContentFormat, part)}
		}

		return resourcePiece(embeddedResourceLabel,
			newResourceContents(part.Resource))
	default:
		return piece{text: fmt.Sprintf(unknownContentFormat, part)}
	}
}

func newResourceLink(link *sdk.ResourceLink) ResourceLink {
	converted := ResourceLink{
		URI: link.URI, Name: link.Name, Title: link.Title,
		Description: link.Description, MIMEType: link.MIMEType,
	}

	if link.Size != nil {
		converted.Size = *link.Size
	}

	return converted
}

// resourceLinkNote is a link as one line the model can read, without the
// description, which can be long.
func resourceLinkNote(link ResourceLink) string {
	var note strings.Builder

	note.WriteString(resourceLinkNoteStart)
	note.WriteString(link.URI)

	if link.Name != "" {
		fmt.Fprintf(&note, resourceLinkNameFormat, link.Name)
	}

	if link.Title != "" {
		fmt.Fprintf(&note, resourceLinkTitle, link.Title)
	}

	if link.MIMEType != "" {
		fmt.Fprintf(&note, resourceLinkMIMEType, link.MIMEType)
	}

	if link.Size > 0 {
		fmt.Fprintf(&note, resourceLinkSize, link.Size)
	}

	note.WriteString(resourceLinkNoteEnd)

	return note.String()
}

// resourcePiece is resource contents under a header naming them. Text
// follows the header, an image in a type every provider takes becomes an
// image, and any other blob is described by type and size.
func resourcePiece(label string, contents ResourceContents) piece {
	if contents.Blob == nil {
		return piece{text: resourceHeader(label, contents) +
			resultPartSeparator + contents.Text}
	}

	if isProviderImage(contents.MIMEType) {
		return piece{
			text: resourceHeader(label, contents),
			image: &elelem.ImageSource{
				Data: contents.Blob, MediaType: contents.MIMEType,
			},
		}
	}

	mimeType := contents.MIMEType
	if mimeType == "" {
		mimeType = unknownMIMEType
	}

	return piece{text: fmt.Sprintf(binaryResourceFormat,
		label, contents.URI, mimeType, len(contents.Blob))}
}

func resourceHeader(label string, contents ResourceContents) string {
	if contents.MIMEType == "" {
		return fmt.Sprintf(resourceHeaderFormat, label, contents.URI)
	}

	return fmt.Sprintf(resourceMIMEFormat, label, contents.URI,
		contents.MIMEType)
}

// isProviderImage reports whether every provider takes images of the type.
func isProviderImage(mimeType string) bool {
	return slices.Contains([]string{
		elelem.MediaTypePNG, elelem.MediaTypeJPEG,
		elelem.MediaTypeGIF, elelem.MediaTypeWebP,
	}, mimeType)
}

// ResourceResult turns read resource contents into a tool result the way
// embedded resources in tool results are converted: each item gets a
// header line naming its URI and type, then its text, or its image in
// Images; any other blob is described by type and size, never shown. It
// suits a tool that reads resources for the model.
func ResourceResult(contents []ResourceContents) elelem.ToolResult {
	result := elelem.ToolResult{}
	parts := make([]string, 0, len(contents))

	for _, item := range contents {
		result.Images, parts = appendPiece(result.Images, parts,
			resourcePiece(readResourceLabel, item))
	}

	result.Content = strings.Join(parts, resultPartSeparator)

	return result
}

// contentSize is how many bytes of text and binary data a part carries.
func contentSize(content sdk.Content) int {
	switch part := content.(type) {
	case *sdk.TextContent:
		return len(part.Text)
	case *sdk.ImageContent:
		return len(part.Data)
	case *sdk.AudioContent:
		return len(part.Data)
	case *sdk.EmbeddedResource:
		if part.Resource == nil {
			return 0
		}

		return len(part.Resource.Text) + len(part.Resource.Blob)
	case *sdk.ResourceLink:
		return len(part.URI) + len(part.Name) + len(part.Title) +
			len(part.Description)
	default:
		return 0
	}
}
