package anthropic

import (
	"encoding/base64"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToUserBlocks_Image(t *testing.T) {
	t.Parallel()

	png := []byte("pretend-png")

	testCases := []struct {
		name      string
		part      elelem.Part
		assertion func(t *testing.T, block anthropicBlock)
	}{
		{
			name: "a link becomes a url source",
			part: elelem.ImageURL("https://example.com/x.png"),
			assertion: func(t *testing.T, block anthropicBlock) {
				t.Helper()

				require.NotNil(t, block.OfImage)
				require.NotNil(t, block.OfImage.Source.OfURL)
				assert.Equal(
					t,
					"https://example.com/x.png",
					block.OfImage.Source.OfURL.URL,
				)
			},
		},
		{
			name: "raw bytes become a base64 source",
			part: elelem.ImageBytes(png, elelem.MediaTypePNG),
			assertion: func(t *testing.T, block anthropicBlock) {
				t.Helper()

				require.NotNil(t, block.OfImage)
				source := block.OfImage.Source.OfBase64
				require.NotNil(t, source)
				assert.Equal(
					t,
					base64.StdEncoding.EncodeToString(png),
					source.Data,
				)
				assert.Equal(
					t,
					elelem.MediaTypePNG,
					string(source.MediaType),
				)
			},
		},
		{
			// OpenAI packs bytes and media type into one url string. Content
			// built for that provider must still reach this one, so the driver
			// unpacks rather than refusing something the model could read.
			name: "an OpenAI-shaped data URI is unpacked",
			part: elelem.ImageURL(
				elelem.ImageSource{
					Data:      png,
					MediaType: elelem.MediaTypePNG,
				}.DataURI(),
			),
			assertion: func(t *testing.T, block anthropicBlock) {
				t.Helper()

				source := block.OfImage.Source.OfBase64
				require.NotNil(
					t, source,
					"a data URI must not go out as a link",
				)
				assert.Equal(
					t,
					base64.StdEncoding.EncodeToString(png),
					source.Data,
				)
				assert.Equal(t, elelem.MediaTypePNG, string(source.MediaType))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			blocks, err := toUserBlocks(elelem.Message{
				Role:    elelem.RoleUser,
				Content: elelem.Content{tc.part},
			})
			require.NoError(t, err)
			require.Len(t, blocks, 1)
			tc.assertion(t, blocks[0])
		})
	}
}

// Every refusal below is LOCAL. The alternative is a provider 400 naming a
// block type the caller never wrote, one round trip later.
func TestToUserBlocks_RefusesLocally(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		part    elelem.Part
		wantErr error
	}{
		{
			// Not a media type we could map — the Messages API has no audio
			// block at all.
			name:    "audio has no anthropic equivalent",
			part:    elelem.AudioBytes([]byte{1}, elelem.AudioFormatWAV),
			wantErr: elelem.ErrUnsupportedContent,
		},
		{
			// SupportsImageInput is true and this still must fail: the base64
			// source accepts exactly four media types.
			name:    "an image media type outside the closed set",
			part:    elelem.ImageBytes([]byte{1}, "image/heic"),
			wantErr: ErrUnsupportedParameter,
		},
		{
			name:    "a document media type outside the closed set",
			part:    elelem.FileBytes([]byte{1}, "application/msword", "a.doc"),
			wantErr: ErrUnsupportedParameter,
		},
		{
			// A file id is provider-scoped; honouring one minted elsewhere is
			// impossible, and dropping the attachment silently is worse.
			name:    "a provider-scoped file id",
			part:    elelem.FileRef("file-abc"),
			wantErr: elelem.ErrUnsupportedContent,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := toUserBlocks(elelem.Message{
				Role:    elelem.RoleUser,
				Content: elelem.Content{tc.part},
			})

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestToUserBlocks_Documents(t *testing.T) {
	t.Parallel()

	pdf := []byte("%PDF-1.7")

	blocks, err := toUserBlocks(elelem.Message{
		Role: elelem.RoleUser,
		Content: elelem.Content{
			elelem.TextOf("summarise this"),
			elelem.FileBytes(pdf, elelem.MediaTypePDF, "doc.pdf"),
			elelem.FileBytes([]byte("plain"), elelem.MediaTypeText, "n.txt"),
		},
	})
	require.NoError(t, err)
	require.Len(t, blocks, 3)

	assert.NotNil(t, blocks[0].OfText)

	require.NotNil(t, blocks[1].OfDocument)
	require.NotNil(t, blocks[1].OfDocument.Source.OfBase64)
	assert.Equal(
		t,
		base64.StdEncoding.EncodeToString(pdf),
		blocks[1].OfDocument.Source.OfBase64.Data,
	)

	require.NotNil(t, blocks[2].OfDocument)
	require.NotNil(t, blocks[2].OfDocument.Source.OfText)
	assert.Equal(t, "plain", blocks[2].OfDocument.Source.OfText.Data)
}

// A malformed part is wrong for every provider, so it must surface as an
// invalid request rather than as an Anthropic limitation.
func TestToUserBlocks_RejectsMalformedContent(t *testing.T) {
	t.Parallel()

	_, err := toUserBlocks(elelem.Message{
		Role: elelem.RoleUser,
		Content: elelem.Content{{
			Type:  elelem.PartTypeImage,
			Image: &elelem.ImageSource{},
		}},
	})

	require.ErrorIs(t, err, elelem.ErrImageSourceAmbiguous)
}

type anthropicBlock = anthropicsdk.ContentBlockParamUnion
