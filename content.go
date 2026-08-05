package elelem

import (
	"encoding/base64"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

// PartType identifies what a Part carries.
//
// A string alias rather than a defined type so a driver can switch on it
// without a conversion, matching Role and FinishReason.
type PartType = string

const (
	PartTypeText  PartType = "text"
	PartTypeImage PartType = "image"
	PartTypeAudio PartType = "audio"
	PartTypeFile  PartType = "file"
)

// ImageDetail is how much fidelity the model should spend on an image.
//
// OpenAI honours it; Anthropic has no equivalent and drops it. It is a hint
// about cost, never about meaning, so dropping it cannot change an answer —
// which is why this is a silent difference rather than a capability gate.
type ImageDetail = string

const (
	ImageDetailAuto ImageDetail = "auto"
	ImageDetailLow  ImageDetail = "low"
	ImageDetailHigh ImageDetail = "high"
)

// AudioFormat is the encoding of an AudioSource's bytes. OpenAI accepts only
// these two.
type AudioFormat = string

const (
	AudioFormatWAV AudioFormat = "wav"
	AudioFormatMP3 AudioFormat = "mp3"
)

// Media types that mean something to a provider rather than to us.
const (
	MediaTypeJPEG = "image/jpeg"
	MediaTypePNG  = "image/png"
	MediaTypeGIF  = "image/gif"
	MediaTypeWebP = "image/webp"
	MediaTypePDF  = "application/pdf"
	MediaTypeText = "text/plain"
)

const dataURIScheme = "data:"

// ImageSource is where an image comes from. Exactly one of URL or Data is set,
// which the constructors guarantee and Validate enforces for a hand-built one.
//
// The providers disagree about shape: OpenAI takes a single `url` that is
// EITHER a link or a `data:` URI, while Anthropic takes a tagged union with the
// media type as its own field. Modelling it Anthropic's way is the direction
// that does not lose information — collapsing a tagged source into a data URI
// is mechanical, recovering a media type from an arbitrary URL is not.
type ImageSource struct {
	// URL is an http(s) link or a `data:<media-type>;base64,<data>` URI.
	URL string `json:"url,omitempty"`

	// Data is the raw image, NOT base64. Drivers encode on the way out, so a
	// caller never encodes by hand and never double-encodes.
	Data []byte `json:"data,omitempty"`

	// MediaType is required alongside Data, optional alongside URL. Anthropic
	// accepts only the four image/* constants above and its driver rejects
	// anything else locally.
	MediaType string `json:"mediaType,omitempty"`

	// Detail is an OpenAI-only fidelity hint.
	Detail ImageDetail `json:"detail,omitempty"`
}

// AudioSource is audio input. OpenAI accepts it; Anthropic's messages API has
// no audio block at all, so its driver rejects the part before any request.
type AudioSource struct {
	Data   []byte      `json:"data,omitempty"`
	Format AudioFormat `json:"format,omitempty"`
}

// FileSource is a document: either the bytes or a provider-side FileID.
//
// The providers cover different ground. OpenAI takes any bytes with a
// filename; Anthropic's document block takes PDF bytes, plain text, or a list
// of content blocks — so a .docx that works on one is rejected locally by the
// other rather than at the API.
type FileSource struct {
	Data      []byte `json:"data,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	Filename  string `json:"filename,omitempty"`

	// FileID references a file already uploaded to the provider. It is
	// provider-scoped: an id from one means nothing to another.
	FileID string `json:"fileId,omitempty"`
}

// Part is one piece of a message's content.
//
// A tagged struct rather than an interface because Message round-trips through
// callers' storage as JSON, and an interface needs custom marshalling on both
// sides to survive that. ProviderReasoning already made the same trade.
type Part struct {
	Type PartType `json:"type"`

	// Text is set when Type is PartTypeText.
	Text string `json:"text,omitempty"`

	Image *ImageSource `json:"image,omitempty"`
	Audio *AudioSource `json:"audio,omitempty"`
	File  *FileSource  `json:"file,omitempty"`
}

// Content is a message's content: an ordered list of parts.
//
// Order is meaningful — providers show the model the parts as given, so an
// image before its question reads differently from after it.
type Content []Part

// Text builds single-part text content. This is the common case by a wide
// margin, so it returns Content rather than Part.
func Text(text string) Content {
	return Content{TextOf(text)}
}

// TextOf builds a text part, for composing multi-part content.
func TextOf(text string) Part {
	return Part{Type: PartTypeText, Text: text}
}

// ImageURL builds an image part from a link, or from a `data:` URI when the
// caller already holds one in that form.
func ImageURL(url string) Part {
	return Part{Type: PartTypeImage, Image: &ImageSource{URL: url}}
}

// ImageBytes builds an image part from raw bytes. The driver base64-encodes on
// the way out — do not pre-encode.
func ImageBytes(data []byte, mediaType string) Part {
	return Part{
		Type:  PartTypeImage,
		Image: &ImageSource{Data: data, MediaType: mediaType},
	}
}

// AudioBytes builds an audio part. OpenAI only — see AudioSource.
func AudioBytes(data []byte, format AudioFormat) Part {
	return Part{
		Type:  PartTypeAudio,
		Audio: &AudioSource{Data: data, Format: format},
	}
}

// FileBytes builds a file part from raw bytes.
func FileBytes(data []byte, mediaType, filename string) Part {
	return Part{
		Type: PartTypeFile,
		File: &FileSource{Data: data, MediaType: mediaType, Filename: filename},
	}
}

// FileRef builds a file part referencing a file already uploaded to the
// provider. The id is provider-scoped.
func FileRef(fileID string) Part {
	return Part{Type: PartTypeFile, File: &FileSource{FileID: fileID}}
}

// String returns the text parts joined by newlines, ignoring the rest.
//
// Deliberately lossy: an image has no text, and substituting a placeholder
// would put words in the model's mouth. Token counting, logging and
// text-only provider fields all read through here.
func (c Content) String() string {
	if len(c) == 1 && c[0].Type == PartTypeText {
		return c[0].Text
	}

	texts := make([]string, 0, len(c))

	for _, part := range c {
		if part.Type == PartTypeText && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}

	return strings.Join(texts, "\n")
}

// IsTextOnly reports whether every part is text, so a text-only path can stay
// exactly as it was.
func (c Content) IsTextOnly() bool {
	for _, part := range c {
		if part.Type != PartTypeText {
			return false
		}
	}

	return true
}

// Types returns the distinct part types present, in first-seen order. The
// capability gate reports on these so a refusal names the kind of content it
// refused rather than an offset.
func (c Content) Types() []PartType {
	seen := make(map[PartType]struct{}, len(c))
	types := make([]PartType, 0, len(c))

	for _, part := range c {
		if _, ok := seen[part.Type]; ok {
			continue
		}

		seen[part.Type] = struct{}{}
		types = append(types, part.Type)
	}

	return types
}

// Clone returns a deep copy. Byte slices are copied too: content crosses into
// engine-owned transcripts that outlive the caller's buffer.
func (c Content) Clone() Content {
	if c == nil {
		return nil
	}

	out := make(Content, len(c))
	for i, part := range c {
		out[i] = part.clone()
	}

	return out
}

func (p Part) clone() Part {
	out := p

	if p.Image != nil {
		image := *p.Image
		image.Data = cloneBytes(p.Image.Data)
		out.Image = &image
	}

	if p.Audio != nil {
		audio := *p.Audio
		audio.Data = cloneBytes(p.Audio.Data)
		out.Audio = &audio
	}

	if p.File != nil {
		file := *p.File
		file.Data = cloneBytes(p.File.Data)
		out.File = &file
	}

	return out
}

func cloneBytes(data []byte) []byte {
	if data == nil {
		return nil
	}

	return append([]byte(nil), data...)
}

// Validate reports the first structural problem in the content.
//
// Structure is checked before capabilities: an image part with neither a URL
// nor bytes is malformed for EVERY provider, while audio is merely unsupported
// by one. Conflating the two would report "Anthropic cannot do this" for
// something no provider could.
func (c Content) Validate() error {
	for i, part := range c {
		if err := part.validate(); err != nil {
			return ctxerrors.Wrapf(err, "part %d", i)
		}
	}

	return nil
}

func (p Part) validate() error {
	if err := p.validatePayloadPresence(); err != nil {
		return err
	}

	switch p.Type {
	case PartTypeText:
		return nil
	case PartTypeImage:
		return p.Image.validate()
	case PartTypeAudio:
		return p.Audio.validate()
	case PartTypeFile:
		return p.File.validate()
	default:
		return ctxerrors.Wrapf(ErrPartTypeUnknown, "%q", p.Type)
	}
}

// validatePayloadPresence checks that the part carries exactly the payload its
// type names — separated from the type switch so neither half has to reason
// about the other's cases.
func (p Part) validatePayloadPresence() error {
	present := map[PartType]bool{
		PartTypeImage: p.Image != nil,
		PartTypeAudio: p.Audio != nil,
		PartTypeFile:  p.File != nil,
	}

	for partType, hasPayload := range present {
		if partType == p.Type && !hasPayload {
			return ctxerrors.Wrap(ErrPartPayloadMissing, partType)
		}

		if partType != p.Type && hasPayload {
			return ctxerrors.Wrap(ErrPartPayloadMismatch, p.Type)
		}
	}

	return nil
}

func (s ImageSource) validate() error {
	hasURL := s.URL != ""
	hasData := len(s.Data) > 0

	// Both or neither: a source that carries two answers is as unusable as one
	// that carries none, and silently preferring one would make the other
	// vanish without a word.
	if hasURL == hasData {
		return ctxerrors.Wrap(ErrImageSourceAmbiguous, "image source")
	}

	if hasData && s.MediaType == "" {
		return ctxerrors.Wrap(ErrImageMediaTypeRequired, "image source")
	}

	return nil
}

func (s AudioSource) validate() error {
	if len(s.Data) == 0 {
		return ctxerrors.Wrap(ErrAudioDataRequired, "audio source")
	}

	if s.Format != AudioFormatWAV && s.Format != AudioFormatMP3 {
		return ctxerrors.Wrapf(ErrAudioFormatUnknown, "%q", s.Format)
	}

	return nil
}

func (s FileSource) validate() error {
	hasData := len(s.Data) > 0
	hasID := s.FileID != ""

	if hasData == hasID {
		return ctxerrors.Wrap(ErrFileSourceAmbiguous, "file source")
	}

	return nil
}

// DataURI renders the source as `data:<media-type>;base64,<data>`, the only
// shape OpenAI accepts for inline image bytes. A source that already carries a
// URL is returned untouched.
func (s ImageSource) DataURI() string {
	if s.URL != "" {
		return s.URL
	}

	return dataURIScheme + s.MediaType + ";base64," +
		base64.StdEncoding.EncodeToString(s.Data)
}

// IsDataURI reports whether the URL carries inline bytes rather than a link.
// Anthropic needs the distinction: a link becomes URLImageSource, a data URI
// has to be split back into media type and payload.
func (s ImageSource) IsDataURI() bool {
	return strings.HasPrefix(s.URL, dataURIScheme)
}

// DecodeDataURI splits a `data:` URL into its media type and raw bytes.
//
// Anthropic has no equivalent of OpenAI's packed form, so a caller who built
// content for one provider and sent it to the other still works — the driver
// unpacks here rather than refusing something the model could have read.
func (s ImageSource) DecodeDataURI() (string, []byte, error) {
	if !s.IsDataURI() {
		return s.MediaType, s.Data, nil
	}

	rest := strings.TrimPrefix(s.URL, dataURIScheme)

	meta, payload, found := strings.Cut(rest, ",")
	if !found {
		return "", nil, ctxerrors.Wrap(ErrDataURIMalformed, "missing comma")
	}

	mediaType, encoding, _ := strings.Cut(meta, ";")
	if encoding != "base64" {
		return "", nil, ctxerrors.Wrapf(
			ErrDataURIMalformed, "encoding %q", encoding,
		)
	}

	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, ctxerrors.Wrap(err, "decode data URI payload")
	}

	return mediaType, data, nil
}

// appendText grows the trailing text part, or starts one.
//
// Streamed deltas arrive as fragments of a single assistant turn, so they
// belong in ONE part — appending a part per delta would produce hundreds of
// them and turn a sentence into a list.
func appendText(content Content, text string) Content {
	if n := len(content); n > 0 && content[n-1].Type == PartTypeText {
		content[n-1].Text += text

		return content
	}

	return append(content, TextOf(text))
}
