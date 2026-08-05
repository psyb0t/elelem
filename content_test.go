package elelem

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContent_StringIgnoresNonTextParts(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		content Content
		want    string
	}{
		{"text only", Text("hello"), "hello"},
		{
			"image contributes nothing",
			Content{TextOf("what is this"), ImageURL("https://x/y.png")},
			"what is this",
		},
		{
			"multiple text parts join with newline",
			Content{TextOf("one"), TextOf("two")},
			"one\ntwo",
		},
		{"image alone yields empty", Content{ImageURL("https://x/y.png")}, ""},
		{"nil content", nil, ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.content.String())
		})
	}
}

func TestContent_Validate(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		content Content
		wantErr error
	}{
		{"text", Text("ok"), nil},
		{"image url", Content{ImageURL("https://x/y.png")}, nil},
		{"image bytes", Content{ImageBytes([]byte{1}, MediaTypePNG)}, nil},
		{
			"image with neither source",
			Content{{Type: PartTypeImage, Image: &ImageSource{}}},
			ErrImageSourceAmbiguous,
		},
		{
			"image with both sources",
			Content{{Type: PartTypeImage, Image: &ImageSource{
				URL:       "https://x/y.png",
				Data:      []byte{1},
				MediaType: MediaTypePNG,
			}}},
			ErrImageSourceAmbiguous,
		},
		{
			"image bytes without media type",
			Content{{
				Type:  PartTypeImage,
				Image: &ImageSource{Data: []byte{1}},
			}},
			ErrImageMediaTypeRequired,
		},
		{
			"image part with no payload",
			Content{{Type: PartTypeImage}},
			ErrPartPayloadMissing,
		},
		{
			"audio with unknown format",
			Content{{Type: PartTypeAudio, Audio: &AudioSource{
				Data: []byte{1}, Format: "flac",
			}}},
			ErrAudioFormatUnknown,
		},
		{"audio ok", Content{AudioBytes([]byte{1}, AudioFormatWAV)}, nil},
		{
			"file with both data and id",
			Content{{Type: PartTypeFile, File: &FileSource{
				Data: []byte{1}, FileID: "f-1",
			}}},
			ErrFileSourceAmbiguous,
		},
		{
			"file bytes",
			Content{FileBytes([]byte{1}, MediaTypePDF, "a.pdf")},
			nil,
		},
		{"file ref", Content{FileRef("f-1")}, nil},
		{"unknown type", Content{{Type: "video"}}, ErrPartTypeUnknown},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.content.Validate()
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// A data URI must survive the round trip, because that is the ONLY way an
// OpenAI-shaped image reaches Anthropic: OpenAI packs bytes and media type into
// one url string, Anthropic needs them apart again.
func TestImageSource_DataURIRoundTrip(t *testing.T) {
	t.Parallel()

	original := ImageSource{
		Data:      []byte("not-really-a-png"),
		MediaType: MediaTypePNG,
	}

	uri := original.DataURI()
	assert.Contains(t, uri, "data:"+MediaTypePNG+";base64,")

	mediaType, data, err := ImageSource{URL: uri}.DecodeDataURI()
	require.NoError(t, err)
	assert.Equal(t, MediaTypePNG, mediaType)
	assert.Equal(t, original.Data, data)
}

func TestImageSource_DataURIPassesLinksThrough(t *testing.T) {
	t.Parallel()

	source := ImageSource{URL: "https://example.com/x.png"}

	assert.Equal(t, source.URL, source.DataURI())
	assert.False(t, source.IsDataURI())
}

// Clone must copy the BYTES, not just the pointer: content crosses into an
// engine-owned transcript that outlives the caller's buffer, and a caller
// reusing that buffer would otherwise rewrite history already sent.
func TestContent_CloneCopiesBytes(t *testing.T) {
	t.Parallel()

	data := []byte{1, 2, 3}
	original := Content{ImageBytes(data, MediaTypePNG)}

	cloned := original.Clone()
	data[0] = 9

	assert.Equal(t, byte(1), cloned[0].Image.Data[0],
		"clone still points at the caller's buffer")
}
