package openai

import (
	"encoding/base64"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

// toUserMessage translates portable content into an OpenAI user message.
//
// Text-only content keeps the plain-string form rather than a one-element part
// array. Both are legal, but the string is what every existing recorded
// fixture and every OpenAI-compatible endpoint has always been sent — some of
// those endpoints implement only the subset they have seen.
func toUserMessage(message elelem.Message) (messageParam, error) {
	if err := message.Content.Validate(); err != nil {
		return messageParam{}, ctxerrors.Wrap(err, "validate content")
	}

	if message.Content.IsTextOnly() {
		return openaisdk.UserMessage(message.Text()), nil
	}

	parts := make(
		[]openaisdk.ChatCompletionContentPartUnionParam,
		0,
		len(message.Content),
	)

	for _, part := range message.Content {
		translated, err := toContentPart(part)
		if err != nil {
			return messageParam{}, err
		}

		parts = append(parts, translated)
	}

	return openaisdk.UserMessage(parts), nil
}

func toContentPart(
	part elelem.Part,
) (openaisdk.ChatCompletionContentPartUnionParam, error) {
	switch part.Type {
	case elelem.PartTypeText:
		return openaisdk.TextContentPart(part.Text), nil
	case elelem.PartTypeImage:
		return toImagePart(*part.Image), nil
	case elelem.PartTypeAudio:
		return toAudioPart(*part.Audio), nil
	case elelem.PartTypeFile:
		return toFilePart(*part.File), nil
	default:
		return openaisdk.ChatCompletionContentPartUnionParam{}, ctxerrors.Wrapf(
			elelem.ErrPartTypeUnknown, "%q", part.Type,
		)
	}
}

// toImagePart builds the image part. OpenAI takes ONE url field that is either
// a link or a data: URI, so raw bytes collapse into the latter — which is why
// the portable model keeps the media type separately and renders it here
// rather than making callers hand-assemble the URI.
func toImagePart(
	source elelem.ImageSource,
) openaisdk.ChatCompletionContentPartUnionParam {
	image := openaisdk.ChatCompletionContentPartImageImageURLParam{
		URL: source.DataURI(),
	}
	if source.Detail != "" {
		image.Detail = source.Detail
	}

	return openaisdk.ImageContentPart(image)
}

func toAudioPart(
	source elelem.AudioSource,
) openaisdk.ChatCompletionContentPartUnionParam {
	return openaisdk.InputAudioContentPart(
		openaisdk.ChatCompletionContentPartInputAudioInputAudioParam{
			Data:   encode(source.Data),
			Format: source.Format,
		},
	)
}

func toFilePart(
	source elelem.FileSource,
) openaisdk.ChatCompletionContentPartUnionParam {
	file := openaisdk.ChatCompletionContentPartFileFileParam{}

	if source.FileID != "" {
		file.FileID = openaisdk.String(source.FileID)

		return openaisdk.FileContentPart(file)
	}

	file.FileData = openaisdk.String(encode(source.Data))
	if source.Filename != "" {
		file.Filename = openaisdk.String(source.Filename)
	}

	return openaisdk.FileContentPart(file)
}

// encode base64s raw bytes. OpenAI's audio and file parts both take an encoded
// string, so callers hand us raw bytes and the encoding happens exactly here.
func encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
