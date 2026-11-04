package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

// convertResult maps a tool result onto elelem's. Text parts join into
// Content and image parts become Images. A part elelem cannot carry becomes
// a short note in Content, so nothing is dropped without saying so.
func convertResult(result *sdk.CallToolResult) (elelem.ToolResult, error) {
	converted := elelem.ToolResult{IsError: result.IsError}
	parts := make([]string, 0, len(result.Content))

	for _, content := range result.Content {
		if image, isImage := content.(*sdk.ImageContent); isImage {
			converted.Images = append(converted.Images, elelem.ImageSource{
				Data: image.Data, MediaType: image.MIMEType,
			})

			continue
		}

		parts = append(parts, contentText(content))
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

	return converted, nil
}

// contentText is a non-image part as text: the text itself, or a note for a
// part elelem cannot carry.
func contentText(content sdk.Content) string {
	switch part := content.(type) {
	case *sdk.TextContent:
		return part.Text
	case *sdk.AudioContent:
		return fmt.Sprintf(audioNoteFormat, part.MIMEType)
	case *sdk.ResourceLink:
		return fmt.Sprintf(resourceLinkNoteFormat, part.URI)
	case *sdk.EmbeddedResource:
		return embeddedResourceText(part)
	default:
		return fmt.Sprintf(unknownContentFormat, part)
	}
}

func embeddedResourceText(part *sdk.EmbeddedResource) string {
	if part.Resource == nil {
		return fmt.Sprintf(unknownContentFormat, part)
	}

	if part.Resource.Text != "" || len(part.Resource.Blob) == 0 {
		return part.Resource.Text
	}

	return fmt.Sprintf(binaryResourceFormat, part.Resource.URI,
		part.Resource.MIMEType)
}
