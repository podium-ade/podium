package podiumv1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestMessageIsWiredWhereTheIndexSaysItIs reads the descriptor rather than trusting the
// generated constants: the enum number and the field tag are the contract with every
// already-stored row and every node and relay built against a different revision. A
// renumbering would be silent everywhere else and catastrophic on the wire.
func TestMessageIsWiredWhereTheIndexSaysItIs(t *testing.T) {
	require.Equal(t, TaskEventKind(10), TaskEventKind_TASK_EVENT_KIND_MESSAGE)

	kinds := TaskEventKind_TASK_EVENT_KIND_MESSAGE.Descriptor().Values()
	value := kinds.ByNumber(10)
	require.NotNil(t, value, "no TaskEventKind carries number 10")
	assert.Equal(t, protoreflect.Name("TASK_EVENT_KIND_MESSAGE"), value.Name())

	fields := (&TaskEvent{}).ProtoReflect().Descriptor().Fields()
	field := fields.ByName("message")
	require.NotNil(t, field, "TaskEvent has no message field")
	assert.Equal(t, protoreflect.FieldNumber(12), field.Number())

	oneof := field.ContainingOneof()
	require.NotNil(t, oneof, "message must live inside the payload oneof: one event, one payload")
	assert.Equal(t, protoreflect.Name("payload"), oneof.Name())

	// And the generated wrapper is the oneof arm, not a plain field.
	ev := &TaskEvent{Payload: &TaskEvent_Message{Message: &Message{Type: "final", Text: "hi"}}}
	require.NotNil(t, ev.GetMessage())
	assert.Equal(t, "final", ev.GetMessage().GetType())
	assert.Nil(t, ev.GetStep(), "a different arm reads back nil")
}
