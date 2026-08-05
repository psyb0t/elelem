package openai

import (
	"encoding/base64"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Text-only content keeps the plain-string form. Both shapes are legal, but
// the string is what every OpenAI-compatible endpoint has always been sent,
// and some implement only the subset they have seen.
func TestToUserMessage_TextOnlyStaysAString(t *testing.T) {
	t.Parallel()

	message, err := toUserMessage(elelem.Message{
		Role:    elelem.RoleUser,
		Content: elelem.Text("hello"),
	})
	require.NoError(t, err)

	require.NotNil(t, message.OfUser)
	assert.Equal(t, "hello", message.OfUser.Content.OfString.Value)
	assert.Nil(t, message.OfUser.Content.OfArrayOfContentParts)
}

func TestToUserMessage_MultimodalParts(t *testing.T) {
	t.Parallel()

	png := []byte("pretend-png")
	wav := []byte("pretend-wav")
	pdf := []byte("%PDF-1.7")

	message, err := toUserMessage(elelem.Message{
		Role: elelem.RoleUser,
		Content: elelem.Content{
			elelem.TextOf("what is all this"),
			elelem.ImageBytes(png, elelem.MediaTypePNG),
			elelem.ImageURL("https://example.com/x.png"),
			elelem.AudioBytes(wav, elelem.AudioFormatWAV),
			elelem.FileBytes(pdf, elelem.MediaTypePDF, "doc.pdf"),
			elelem.FileRef("file-abc"),
		},
	})
	require.NoError(t, err)

	require.NotNil(t, message.OfUser)

	parts := message.OfUser.Content.OfArrayOfContentParts
	require.Len(t, parts, 6)

	require.NotNil(t, parts[0].OfText)
	assert.Equal(t, "what is all this", parts[0].OfText.Text)

	// Raw bytes collapse into a data URI — OpenAI has ONE url field that is
	// either a link or inline data, which is why the portable model keeps the
	// media type separate and renders it here.
	require.NotNil(t, parts[1].OfImageURL)
	assert.Equal(
		t,
		"data:"+elelem.MediaTypePNG+";base64,"+
			base64.StdEncoding.EncodeToString(png),
		parts[1].OfImageURL.ImageURL.URL,
	)

	require.NotNil(t, parts[2].OfImageURL)
	assert.Equal(
		t,
		"https://example.com/x.png",
		parts[2].OfImageURL.ImageURL.URL,
	)

	require.NotNil(t, parts[3].OfInputAudio)
	assert.Equal(
		t,
		base64.StdEncoding.EncodeToString(wav),
		parts[3].OfInputAudio.InputAudio.Data,
	)
	assert.Equal(
		t,
		elelem.AudioFormatWAV,
		parts[3].OfInputAudio.InputAudio.Format,
	)

	require.NotNil(t, parts[4].OfFile)
	assert.Equal(
		t,
		base64.StdEncoding.EncodeToString(pdf),
		parts[4].OfFile.File.FileData.Value,
	)
	assert.Equal(t, "doc.pdf", parts[4].OfFile.File.Filename.Value)

	require.NotNil(t, parts[5].OfFile)
	assert.Equal(t, "file-abc", parts[5].OfFile.File.FileID.Value)
}

// Detail is an OpenAI-only fidelity hint, so it must actually reach the wire
// here even though Anthropic drops it.
func TestToUserMessage_CarriesImageDetail(t *testing.T) {
	t.Parallel()

	part := elelem.ImageURL("https://example.com/x.png")
	part.Image.Detail = elelem.ImageDetailLow

	message, err := toUserMessage(elelem.Message{
		Role:    elelem.RoleUser,
		Content: elelem.Content{part},
	})
	require.NoError(t, err)

	parts := message.OfUser.Content.OfArrayOfContentParts
	require.Len(t, parts, 1)
	assert.Equal(
		t,
		elelem.ImageDetailLow,
		parts[0].OfImageURL.ImageURL.Detail,
	)
}

func TestToUserMessage_RejectsMalformedContent(t *testing.T) {
	t.Parallel()

	_, err := toUserMessage(elelem.Message{
		Role: elelem.RoleUser,
		Content: elelem.Content{{
			Type:  elelem.PartTypeAudio,
			Audio: &elelem.AudioSource{Data: []byte{1}, Format: "flac"},
		}},
	})

	require.ErrorIs(t, err, elelem.ErrAudioFormatUnknown)
}
