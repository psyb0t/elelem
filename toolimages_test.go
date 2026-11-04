package elelem

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const toolImageTestModel = "test-model"

func imageToolRequest(images []ImageSource, acceptsImages bool) *Request {
	driver := &scriptedDriver{
		turns:        toolCallTurn(probeToolName, emptyToolArgs),
		capabilities: Capabilities{SupportsImageInput: acceptsImages},
	}

	client := New(driver, WithDefaultModel(Model{ID: toolImageTestModel}))

	return NewRequest(client).
		WithPrompt(NewPrompt().UserText("run")).
		WithTool(Tool{
			Name: probeToolName,
			Handler: func(context.Context, ToolInput) (ToolResult, error) {
				return ToolResult{Content: "screen", Images: images}, nil
			},
		}).
		WithAutoToolCalls()
}

func TestToolResultImagesReachTheToolMessage(t *testing.T) {
	t.Parallel()

	png := ImageSource{Data: []byte("png"), MediaType: MediaTypePNG}
	link := ImageSource{URL: "https://example.com/a.png"}

	response, err := imageToolRequest(
		[]ImageSource{png, link}, true,
	).Run(context.Background())
	require.NoError(t, err)
	require.Len(t, response.Messages, 4)

	result := response.Messages[2]
	assert.Equal(t, RoleTool, result.Role)
	assert.Equal(t, "screen", result.Text())
	assert.Equal(t, []ImageSource{png, link}, result.ToolImages())
}

func TestToolResultImagesTheModelCannotTakeBecomeNotes(t *testing.T) {
	t.Parallel()

	png := ImageSource{Data: []byte("png"), MediaType: MediaTypePNG}

	testCases := []struct {
		name          string
		images        []ImageSource
		acceptsImages bool
		wantImages    []ImageSource
		wantNotes     []string
	}{
		{
			name:       "no image input",
			images:     []ImageSource{png},
			wantImages: []ImageSource{},
			wantNotes: []string{
				`[image 1 of 1 not shown: model "test-model" ` +
					`does not accept image input]`,
			},
		},
		{
			name: "malformed images",
			images: []ImageSource{
				png,
				{Data: []byte("x")},
				{},
			},
			acceptsImages: true,
			wantImages:    []ImageSource{png},
			wantNotes: []string{
				"[image 2 of 3 not shown: " +
					ErrImageMediaTypeRequired.Error() + "]",
				"[image 3 of 3 not shown: " +
					ErrImageSourceAmbiguous.Error() + "]",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			response, err := imageToolRequest(
				tc.images, tc.acceptsImages,
			).Run(context.Background())
			require.NoError(t, err)

			result := response.Messages[2]
			assert.Equal(t, tc.wantImages, result.ToolImages())
			assert.True(t, result.Content.Validate() == nil)

			for _, note := range tc.wantNotes {
				assert.Contains(t, result.Text(), note)
			}

			assert.NotContains(t, result.Text(), ".go:",
				"notes carry no source locations")
		})
	}
}

func TestToolImagesOnlyReadsToolMessages(t *testing.T) {
	t.Parallel()

	user := Message{
		Role:    RoleUser,
		Content: Content{ImageBytes([]byte("png"), MediaTypePNG)},
	}

	assert.Nil(t, user.ToolImages())
}
