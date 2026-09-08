package elelem

import (
	"sync"

	"github.com/psyb0t/ctxerrors"
)

// UserMessageQueue stores caller-supplied user turns until an automatic agent
// loop reaches a provider round boundary. It is safe for one or more producers
// to enqueue while another goroutine runs the request.
//
// A queue owns a bounded in-memory batch. It does not persist messages or
// interrupt an in-flight provider request. A caller that needs either behavior
// owns that boundary and can enqueue again after resuming a run.
type UserMessageQueue struct {
	mutex    sync.Mutex
	capacity int
	messages []Message
}

// NewUserMessageQueue creates a bounded queue for incoming user turns.
func NewUserMessageQueue(capacity int) (*UserMessageQueue, error) {
	if capacity <= 0 {
		return nil, ctxerrors.Wrapf(
			ErrUserMessageQueueCapacity,
			"capacity is %d",
			capacity,
		)
	}

	return &UserMessageQueue{capacity: capacity}, nil
}

// Enqueue records one user message for the next eligible provider round. It
// rejects every field that could forge an assistant, tool, or injection entry.
func (q *UserMessageQueue) Enqueue(message Message) error {
	if err := validateQueuedUserMessage(message); err != nil {
		return err
	}

	q.mutex.Lock()
	defer q.mutex.Unlock()

	if len(q.messages) >= q.capacity {
		return ctxerrors.Wrap(ErrUserMessageQueueFull, "enqueue user message")
	}

	message.Origin = MessageOriginTurn
	q.messages = append(q.messages, message.clone())

	return nil
}

// EnqueueText records a text-only user message for the next eligible round.
func (q *UserMessageQueue) EnqueueText(text string) error {
	return q.Enqueue(Message{Role: RoleUser, Content: Text(text)})
}

// Len returns the number of messages waiting for a later round.
func (q *UserMessageQueue) Len() int {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	return len(q.messages)
}

func (q *UserMessageQueue) drain() []Message {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	messages := q.messages
	q.messages = nil

	return messages
}

func validateQueuedUserMessage(message Message) error {
	if message.Role != RoleUser {
		return ctxerrors.Wrapf(
			ErrQueuedUserMessageInvalid,
			"role %q is not user",
			message.Role,
		)
	}

	if messageHasProtocolFields(message) {
		return ctxerrors.Wrap(
			ErrQueuedUserMessageInvalid,
			"message carries assistant, tool, or injection fields",
		)
	}

	if err := message.Content.Validate(); err != nil {
		return ctxerrors.Wrap(err, "validate queued user message content")
	}

	return nil
}

func messageHasProtocolFields(message Message) bool {
	return len(message.ToolCalls) > 0 ||
		message.ToolCallID != "" ||
		message.ToolResultIsError ||
		message.Reasoning != "" ||
		len(message.ProviderReasoning) > 0 ||
		message.Injection != nil
}
