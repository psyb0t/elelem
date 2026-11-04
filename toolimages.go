package elelem

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// toolImageNoteFormat replaces an image a tool returned that the request
	// cannot carry. The model reads it in the tool result, so it learns the
	// image existed instead of answering as if the tool returned only text.
	toolImageNoteFormat = "[image %d of %d not shown: %s]"

	toolImageNoImageInputFormat = "model %q does not accept image input"
	toolImageInvalid            = "the image is malformed"
	toolImageNoteSeparator      = "\n"
)

// toolResultMessageContent is a tool message's content: the text, then every
// image the model can take. Each image left out gets a note in the text.
func toolResultMessageContent(
	text string,
	images []ImageSource,
	acceptsImages bool,
	modelID string,
) Content {
	if len(images) == 0 {
		return Text(text)
	}

	parts := make([]Part, 0, len(images))
	notes := make([]string, 0)

	for index, image := range images {
		reason := toolImageRefusal(image, acceptsImages, modelID)
		if reason != "" {
			notes = append(notes, fmt.Sprintf(
				toolImageNoteFormat, index+1, len(images), reason,
			))

			continue
		}

		source := image
		parts = append(parts, Part{Type: PartTypeImage, Image: &source})
	}

	if len(notes) > 0 {
		text = strings.Join(append([]string{text}, notes...),
			toolImageNoteSeparator)
	}

	return append(Content{TextOf(text)}, parts...)
}

// toolImageRefusal says why an image cannot go to the model, or "" when it
// can.
func toolImageRefusal(
	image ImageSource,
	acceptsImages bool,
	modelID string,
) string {
	if !acceptsImages {
		return fmt.Sprintf(toolImageNoImageInputFormat, modelID)
	}

	err := image.validate()
	if err == nil {
		return ""
	}

	// The sentinel's text, not err's: ctxerrors appends source locations,
	// which are not for the model.
	for _, sentinel := range []error{
		ErrImageSourceAmbiguous,
		ErrImageMediaTypeRequired,
	} {
		if errors.Is(err, sentinel) {
			return sentinel.Error()
		}
	}

	return toolImageInvalid
}

// ToolImages returns the images a tool message carries, in order.
func (m Message) ToolImages() []ImageSource {
	if m.Role != RoleTool {
		return nil
	}

	images := make([]ImageSource, 0)

	for _, part := range m.Content {
		if part.Type == PartTypeImage && part.Image != nil {
			images = append(images, *part.Image)
		}
	}

	return images
}
