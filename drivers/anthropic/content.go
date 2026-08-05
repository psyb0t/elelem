package anthropic

import (
	"encoding/base64"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

// anthropicImageMediaTypes is the closed set Base64ImageSourceParam accepts.
//
// A per-VALUE gate, not a per-capability one: SupportsImageInput is true and
// still image/heic must be refused, exactly as MaxReasoningEffort passes the
// rank check while the driver rejects a level the model does not take. OpenAI
// accepts anything a data URI can name, so this is a genuine divergence rather
// than a shared restriction.
func anthropicImageMediaTypes() map[string]anthropicsdk.Base64ImageSourceMediaType { //nolint:lll // one generated SDK type name, unbreakable
	return map[string]anthropicsdk.Base64ImageSourceMediaType{
		elelem.MediaTypeJPEG: anthropicsdk.Base64ImageSourceMediaTypeImageJPEG,
		elelem.MediaTypePNG:  anthropicsdk.Base64ImageSourceMediaTypeImagePNG,
		elelem.MediaTypeGIF:  anthropicsdk.Base64ImageSourceMediaTypeImageGIF,
		elelem.MediaTypeWebP: anthropicsdk.Base64ImageSourceMediaTypeImageWebP,
	}
}

// toUserBlocks translates portable content into Anthropic content blocks.
//
// Every refusal here is LOCAL. The alternative is shipping a request the
// provider rejects with a message about a block type the caller never wrote,
// a round-trip later.
func toUserBlocks(
	message elelem.Message,
) ([]anthropicsdk.ContentBlockParamUnion, error) {
	if err := message.Content.Validate(); err != nil {
		return nil, ctxerrors.Wrap(err, "validate content")
	}

	blocks := make(
		[]anthropicsdk.ContentBlockParamUnion,
		0,
		len(message.Content),
	)

	for _, part := range message.Content {
		block, err := toContentBlock(part)
		if err != nil {
			return nil, err
		}

		blocks = append(blocks, block)
	}

	return blocks, nil
}

func toContentBlock(
	part elelem.Part,
) (anthropicsdk.ContentBlockParamUnion, error) {
	switch part.Type {
	case elelem.PartTypeText:
		return anthropicsdk.NewTextBlock(part.Text), nil
	case elelem.PartTypeImage:
		return toImageBlock(*part.Image)
	case elelem.PartTypeAudio:
		// Anthropic's messages API has no audio block at all — not a media
		// type we could map, an absent concept. Nothing to gate per-value.
		return anthropicsdk.ContentBlockParamUnion{}, ctxerrors.Wrap(
			elelem.ErrUnsupportedContent,
			"anthropic has no audio block; send audio to an OpenAI model",
		)
	case elelem.PartTypeFile:
		return toDocumentBlock(*part.File)
	default:
		return anthropicsdk.ContentBlockParamUnion{}, ctxerrors.Wrapf(
			elelem.ErrPartTypeUnknown, "%q", part.Type,
		)
	}
}

func toImageBlock(
	source elelem.ImageSource,
) (anthropicsdk.ContentBlockParamUnion, error) {
	// A plain link goes through as a URL source. Only a data: URI has to be
	// unpacked, because Anthropic keeps the media type in its own field.
	if source.URL != "" && !source.IsDataURI() {
		return anthropicsdk.NewImageBlock(
			anthropicsdk.URLImageSourceParam{URL: source.URL},
		), nil
	}

	mediaType, data, err := source.DecodeDataURI()
	if err != nil {
		return anthropicsdk.ContentBlockParamUnion{},
			ctxerrors.Wrap(err, "decode image source")
	}

	sdkMediaType, ok := anthropicImageMediaTypes()[mediaType]
	if !ok {
		return anthropicsdk.ContentBlockParamUnion{}, ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"image media type %q; anthropic accepts jpeg, png, gif and webp",
			mediaType,
		)
	}

	return anthropicsdk.NewImageBlockBase64(
		string(sdkMediaType), encode(data),
	), nil
}

func toDocumentBlock(
	source elelem.FileSource,
) (anthropicsdk.ContentBlockParamUnion, error) {
	// A file id is provider-scoped and Anthropic's document block has no
	// equivalent field, so an id minted by another provider cannot be honoured
	// — refusing beats silently dropping the attachment.
	if source.FileID != "" {
		return anthropicsdk.ContentBlockParamUnion{}, ctxerrors.Wrap(
			elelem.ErrUnsupportedContent,
			"anthropic has no file-id document source; send the bytes",
		)
	}

	switch source.MediaType {
	case elelem.MediaTypePDF:
		return anthropicsdk.NewDocumentBlock(
			anthropicsdk.Base64PDFSourceParam{Data: encode(source.Data)},
		), nil
	case elelem.MediaTypeText:
		return anthropicsdk.NewDocumentBlock(
			anthropicsdk.PlainTextSourceParam{Data: string(source.Data)},
		), nil
	default:
		return anthropicsdk.ContentBlockParamUnion{}, ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"document media type %q; anthropic accepts %s and %s",
			source.MediaType, elelem.MediaTypePDF, elelem.MediaTypeText,
		)
	}
}

// encode base64s raw bytes. The SDK's Base64 source params take an already
// encoded string (its own parameter is named encodedData), so callers hand us
// raw bytes and the encoding happens exactly here — once, on the way out.
func encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
