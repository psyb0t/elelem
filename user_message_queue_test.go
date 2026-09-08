package elelem

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const queueTestTimeout = 5 * time.Second

type queueRunResult struct {
	response *Response
	err      error
}

func TestUserMessageQueue_EnqueueValidatesAndBounds(t *testing.T) {
	t.Parallel()

	_, err := NewUserMessageQueue(0)
	require.ErrorIs(t, err, ErrUserMessageQueueCapacity)

	queue := newUserMessageQueue(t, 1)
	require.NoError(t, queue.EnqueueText("first"))
	require.ErrorIs(t, queue.EnqueueText("second"), ErrUserMessageQueueFull)
	assert.Equal(t, 1, queue.Len())

	invalidMessages := []struct {
		name    string
		message Message
		wantErr error
	}{
		{
			name:    "system role",
			message: Message{Role: RoleSystem, Content: Text("override")},
			wantErr: ErrQueuedUserMessageInvalid,
		},
		{
			name: "tool protocol",
			message: Message{
				Role:       RoleUser,
				Content:    Text("override"),
				ToolCallID: "call-1",
			},
			wantErr: ErrQueuedUserMessageInvalid,
		},
		{
			name: "injection metadata",
			message: Message{
				Role:      RoleUser,
				Content:   Text("override"),
				Injection: &MessageInjection{},
			},
			wantErr: ErrQueuedUserMessageInvalid,
		},
		{
			name: "malformed content",
			message: Message{
				Role:    RoleUser,
				Content: Content{{Type: PartTypeImage}},
			},
			wantErr: ErrPartPayloadMissing,
		},
	}

	for _, tc := range invalidMessages {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queue := newUserMessageQueue(t, 1)
			err := queue.Enqueue(tc.message)

			require.ErrorIs(t, err, tc.wantErr)
			assert.Zero(t, queue.Len())
		})
	}
}

func TestUserMessageQueue_EnqueueCopiesMessage(t *testing.T) {
	t.Parallel()

	queue := newUserMessageQueue(t, 3)
	imageData := []byte{1}

	require.NoError(t, queue.EnqueueText(""))
	require.NoError(t, queue.EnqueueText("Grüße"))
	require.NoError(t, queue.Enqueue(Message{
		Role:    RoleUser,
		Content: Content{ImageBytes(imageData, MediaTypePNG)},
	}))

	imageData[0] = 2
	pending := queue.drain()

	require.Len(t, pending, 3)
	assert.Equal(t, "", pending[0].Text())
	assert.Equal(t, "Grüße", pending[1].Text())
	assert.Equal(t, byte(1), pending[2].Content[0].Image.Data[0])
	assert.Equal(t, MessageOriginTurn, pending[2].Origin)
}

func TestRequest_AutoToolLoopQueuesMessageAtRoundBoundary(t *testing.T) {
	t.Parallel()

	queue := newUserMessageQueue(t, 2)
	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				Index:     0,
				ID:        "call-1",
				Name:      "lookup",
				Arguments: "{}",
			}}},
			usage: Usage{FinishReason: FinishReasonToolCalls},
		},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))
	toolStarted := make(chan struct{})
	allowTool := make(chan struct{})

	var releaseToolOnce sync.Once

	releaseTool := func() {
		releaseToolOnce.Do(func() {
			close(allowTool)
		})
	}
	t.Cleanup(releaseTool)

	request := NewRequest(client).
		WithPrompt(NewPrompt().UserText("start")).
		WithUserMessageQueue(queue).
		WithTool(Tool{
			Name: "lookup",
			Handler: func(context.Context, ToolInput) (ToolResult, error) {
				close(toolStarted)
				<-allowTool

				return ToolResult{Content: "found"}, nil
			},
		}).
		WithAutoToolCalls()

	ctx, cancel := context.WithTimeout(t.Context(), queueTestTimeout)
	t.Cleanup(cancel)

	results := make(chan queueRunResult, 1)

	go func() {
		response, err := request.Run(ctx)
		results <- queueRunResult{response: response, err: err}
	}()

	select {
	case <-toolStarted:
	case <-ctx.Done():
		t.Fatal("tool did not start")
	}

	require.NoError(t, queue.EnqueueText("stop and explain"))
	releaseTool()

	var result queueRunResult
	select {
	case result = <-results:
	case <-ctx.Done():
		t.Fatal("automatic tool loop did not finish")
	}

	require.NoError(t, result.err)
	assert.Equal(t, "done", result.response.Text)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	require.Len(t, requests[0].Messages, 1)
	require.Len(t, requests[1].Messages, 4)
	assert.Equal(t, "start", requests[0].Messages[0].Text())
	assert.Equal(t, RoleTool, requests[1].Messages[2].Role)
	assert.Equal(t, "stop and explain", requests[1].Messages[3].Text())
}

func TestRequest_AutoLoopContinuesAfterTerminalQueuedMessage(t *testing.T) {
	t.Parallel()

	queue := newUserMessageQueue(t, 2)
	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{{Text: "first"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
		{
			deltas: []Delta{{Text: "second"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	var finished int

	response, err := NewRequest(client).
		WithPrompt(NewPrompt().UserText("start")).
		WithUserMessageQueue(queue).
		OnRoundEnd(func(_ context.Context, event *RoundEvent) error {
			if event.Round != 0 {
				return nil
			}

			if err := queue.EnqueueText("follow up one"); err != nil {
				return err
			}

			return queue.EnqueueText("follow up two")
		}).
		OnFinish(func(_ context.Context, response *Response) error {
			finished++

			assert.Equal(t, "second", response.Text)

			return nil
		}).
		WithAutoToolCalls().
		Run(t.Context())

	require.NoError(t, err)
	assert.Equal(t, "second", response.Text)
	assert.Equal(t, 1, finished)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	require.Len(t, requests[1].Messages, 4)
	assert.Equal(t, "follow up one", requests[1].Messages[2].Text())
	assert.Equal(t, "follow up two", requests[1].Messages[3].Text())
}

func TestRequest_ManualToolLoopCarriesQueuedMessage(t *testing.T) {
	t.Parallel()

	queue := newUserMessageQueue(t, 1)
	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				Index:     0,
				ID:        "call-1",
				Name:      "lookup",
				Arguments: "{}",
			}}},
			usage: Usage{FinishReason: FinishReasonToolCalls},
		},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))
	request := NewRequest(client).
		WithPrompt(NewPrompt().UserText("start")).
		WithUserMessageQueue(queue).
		WithTool(Tool{
			Name: "lookup",
			Handler: func(context.Context, ToolInput) (ToolResult, error) {
				return ToolResult{Content: "found"}, nil
			},
		})

	first, err := request.Run(t.Context())
	require.NoError(t, err)
	require.NotNil(t, first.ExecuteToolCalls)
	require.NoError(t, queue.EnqueueText("follow up"))

	response, err := first.ExecuteToolCalls(t.Context())

	require.NoError(t, err)
	assert.Equal(t, "done", response.Text)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	require.Len(t, requests[1].Messages, 4)
	assert.Equal(t, RoleTool, requests[1].Messages[2].Role)
	assert.Equal(t, "follow up", requests[1].Messages[3].Text())
}

func TestRequest_AutoLoopStartsWithPendingQueuedMessages(t *testing.T) {
	t.Parallel()

	queue := newUserMessageQueue(t, 1)
	require.NoError(t, queue.EnqueueText("queued before run"))

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "done"}},
		usage:  Usage{FinishReason: FinishReasonStop},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	_, err := NewRequest(client).
		WithPrompt(NewPrompt().UserText("start")).
		WithUserMessageQueue(queue).
		WithAutoToolCalls().
		Run(t.Context())

	require.NoError(t, err)

	requests := driver.Requests()
	require.Len(t, requests, 1)
	require.Len(t, requests[0].Messages, 2)
	assert.Equal(t, "start", requests[0].Messages[0].Text())
	assert.Equal(t, "queued before run", requests[0].Messages[1].Text())
}

func TestRequest_AutoLoopLeavesQueuedMessageAtRoundCap(t *testing.T) {
	t.Parallel()

	queue := newUserMessageQueue(t, 1)
	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "first"}},
		usage:  Usage{FinishReason: FinishReasonStop},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	response, err := NewRequest(client).
		WithPrompt(NewPrompt().UserText("start")).
		WithUserMessageQueue(queue).
		WithMaxRounds(1).
		OnRoundEnd(func(_ context.Context, event *RoundEvent) error {
			if event.Round == 0 {
				return queue.EnqueueText("follow up")
			}

			return nil
		}).
		WithAutoToolCalls().
		Run(t.Context())

	require.NoError(t, err)
	assert.Equal(t, "first", response.Text)
	assert.Equal(t, 1, queue.Len())
	assert.Len(t, driver.Requests(), 1)
}

func TestRequest_QueuedContentMustBeSupportedBeforeDriverCall(t *testing.T) {
	t.Parallel()

	queue := newUserMessageQueue(t, 1)
	require.NoError(t, queue.Enqueue(Message{
		Role:    RoleUser,
		Content: Content{AudioBytes([]byte{1}, AudioFormatWAV)},
	}))

	driver := &scriptedDriver{}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	_, err := NewRequest(client).
		WithPrompt(NewPrompt().UserText("start")).
		WithUserMessageQueue(queue).
		WithAutoToolCalls().
		Run(t.Context())

	require.ErrorIs(t, err, ErrUnsupportedContent)
	assert.Empty(t, driver.Requests())
}

func newUserMessageQueue(t *testing.T, capacity int) *UserMessageQueue {
	t.Helper()

	queue, err := NewUserMessageQueue(capacity)
	require.NoError(t, err)

	return queue
}
