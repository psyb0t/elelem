package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

// Prompt is a prompt template a server offers. Every field is
// server-written.
type Prompt struct {
	Name        string
	Title       string
	Description string
	Arguments   []PromptArgument
}

// PromptArgument is one value a prompt takes.
type PromptArgument struct {
	Name        string
	Title       string
	Description string
	Required    bool
}

// PromptPage is one page of a server's prompts. NextCursor, when not
// empty, fetches the next page.
type PromptPage struct {
	Prompts    []Prompt
	NextCursor string
}

// PromptMessage is one message of a filled-in prompt. Content holds text
// parts and image parts. Resource links, embedded resources, and audio
// become text the way they do in tool results.
type PromptMessage struct {
	Role    Role
	Content elelem.Content
}

// PromptResult is a filled-in prompt. It is server-written text.
type PromptResult struct {
	Description string
	Messages    []PromptMessage
}

// CompletionRequest asks for values for one argument of a prompt or a
// resource template. Set exactly one of Prompt, the prompt's name, and
// ResourceTemplate, the template's URI template.
type CompletionRequest struct {
	Prompt           string
	ResourceTemplate string
	// Argument is the argument's name and Value what the user typed so far.
	Argument string
	Value    string
	// Arguments holds the values of arguments already filled in.
	Arguments map[string]string
}

// Completion is the values a server suggests. Total, when not zero, is
// how many match in all, and HasMore says there are more than Values.
type Completion struct {
	Values  []string
	Total   int
	HasMore bool
}

// ListPrompts lists one page of the server's prompts. An empty cursor
// fetches the first page and clears Changed(ListKindPrompts). A server
// without prompts fails with ErrNotSupported.
func (c *Connection) ListPrompts(
	ctx context.Context,
	cursor string,
) (PromptPage, error) {
	err := c.ready(c.info.Capabilities.Prompts, featurePrompts)
	if err != nil {
		return PromptPage{}, err
	}

	var result *sdk.ListPromptsResult

	err = c.listing(ListKindPrompts, cursor, func() error {
		var err error

		result, err = c.session.ListPrompts(ctx,
			&sdk.ListPromptsParams{Cursor: cursor})

		return requestError(err, "list MCP prompts")
	})
	if err != nil {
		return PromptPage{}, err
	}

	page := PromptPage{
		Prompts:    make([]Prompt, 0, len(result.Prompts)),
		NextCursor: result.NextCursor,
	}

	for _, prompt := range result.Prompts {
		page.Prompts = append(page.Prompts, newPrompt(prompt))
	}

	return page, nil
}

func newPrompt(prompt *sdk.Prompt) Prompt {
	converted := Prompt{
		Name: prompt.Name, Title: prompt.Title,
		Description: prompt.Description,
		Arguments:   make([]PromptArgument, 0, len(prompt.Arguments)),
	}

	for _, argument := range prompt.Arguments {
		if argument == nil {
			continue
		}

		converted.Arguments = append(converted.Arguments, PromptArgument{
			Name: argument.Name, Title: argument.Title,
			Description: argument.Description, Required: argument.Required,
		})
	}

	return converted
}

// GetPrompt fills in the prompt named name with arguments. A prompt larger
// than Server.MaxPromptBytes fails with ErrResultTooLarge, and a server
// without prompts with ErrNotSupported.
func (c *Connection) GetPrompt(
	ctx context.Context,
	name string,
	arguments map[string]string,
) (PromptResult, error) {
	err := c.ready(c.info.Capabilities.Prompts, featurePrompts)
	if err != nil {
		return PromptResult{}, err
	}

	if name == "" {
		return PromptResult{}, ctxerrors.Wrap(elelem.ErrInvalidRequest,
			"the prompt name is empty")
	}

	result, err := c.session.GetPrompt(ctx, &sdk.GetPromptParams{
		Name: name, Arguments: arguments,
	})
	if err != nil {
		return PromptResult{}, requestError(err, "get MCP prompt")
	}

	maxBytes := limit(c.server.MaxPromptBytes, DefaultMaxPromptBytes)

	size := len(result.Description)
	for _, message := range result.Messages {
		if message != nil {
			size += contentSize(message.Content)
		}
	}

	if size > maxBytes {
		return PromptResult{}, ctxerrors.Wrapf(ErrResultTooLarge,
			"prompt %s is over %d bytes", name, maxBytes)
	}

	converted := PromptResult{
		Description: result.Description,
		Messages:    make([]PromptMessage, 0, len(result.Messages)),
	}

	for _, message := range result.Messages {
		if message == nil {
			continue
		}

		converted.Messages = append(converted.Messages, PromptMessage{
			Role:    string(message.Role),
			Content: pieceContent(contentPiece(message.Content)),
		})
	}

	return converted, nil
}

// pieceContent is a converted part as elelem content: its text, then its
// image.
func pieceContent(part piece) elelem.Content {
	content := make(elelem.Content, 0, 1)

	if part.text != "" {
		content = append(content, elelem.TextOf(part.text))
	}

	if part.image != nil {
		content = append(content,
			elelem.ImageBytes(part.image.Data, part.image.MediaType))
	}

	return content
}

// Complete asks the server for values for a prompt or resource template
// argument. A server without completions fails with ErrNotSupported.
func (c *Connection) Complete(
	ctx context.Context,
	request CompletionRequest,
) (Completion, error) {
	err := c.ready(c.info.Capabilities.Completions, featureCompletions)
	if err != nil {
		return Completion{}, err
	}

	reference, err := completionReference(request)
	if err != nil {
		return Completion{}, err
	}

	params := &sdk.CompleteParams{
		Ref: reference,
		Argument: sdk.CompleteParamsArgument{
			Name: request.Argument, Value: request.Value,
		},
	}

	if len(request.Arguments) > 0 {
		params.Context = &sdk.CompleteContext{Arguments: request.Arguments}
	}

	result, err := c.session.Complete(ctx, params)
	if err != nil {
		return Completion{}, requestError(err, "complete MCP argument")
	}

	return Completion{
		Values:  result.Completion.Values,
		Total:   result.Completion.Total,
		HasMore: result.Completion.HasMore,
	}, nil
}

func completionReference(
	request CompletionRequest,
) (*sdk.CompleteReference, error) {
	hasPrompt := request.Prompt != ""
	hasTemplate := request.ResourceTemplate != ""

	if hasPrompt == hasTemplate {
		return nil, ctxerrors.Wrap(elelem.ErrInvalidRequest,
			"set exactly one of Prompt and ResourceTemplate")
	}

	if request.Argument == "" {
		return nil, ctxerrors.Wrap(elelem.ErrInvalidRequest,
			"the argument name is empty")
	}

	if hasPrompt {
		return &sdk.CompleteReference{
			Type: completionReferencePrompt, Name: request.Prompt,
		}, nil
	}

	return &sdk.CompleteReference{
		Type: completionReferenceResource, URI: request.ResourceTemplate,
	}, nil
}
