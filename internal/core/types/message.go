package types

import "encoding/json"

// MsgRole is who authored a message.
type MsgRole string

const (
	RoleUser      MsgRole = "user"
	RoleAssistant MsgRole = "assistant"
)

// BlockType is the kind of a content block.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
	// BlockThinking is opaque provider reasoning that must be replayed unchanged.
	BlockThinking BlockType = "thinking"
)

// Block is a provider-neutral content block.
type Block struct {
	Type BlockType `json:"type" bson:"type"`
	Text string    `json:"text,omitempty" bson:"text,omitempty"`

	// tool_use
	ToolUseID string          `json:"tool_use_id,omitempty" bson:"tool_use_id,omitempty"`
	Name      string          `json:"name,omitempty" bson:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty" bson:"input,omitempty"`

	// tool_result (ToolUseID refers to the call)
	IsError bool `json:"is_error,omitempty" bson:"is_error,omitempty"`

	// Raw keeps a provider-specific block (e.g. a thinking signature) so it can
	// be sent back byte-for-byte. Provider names which adapter produced it.
	Raw      json.RawMessage `json:"raw,omitempty" bson:"raw,omitempty"`
	Provider string          `json:"provider,omitempty" bson:"provider,omitempty"`
}

// Text builds a text block.
func Text(s string) Block { return Block{Type: BlockText, Text: s} }

// Message is one append-only entry of session history.
type Message struct {
	Meta      `bson:",inline"`
	SessionID string  `json:"session_id" bson:"session_id"`
	TurnID    string  `json:"turn_id" bson:"turn_id"`
	Seq       int64   `json:"seq" bson:"seq"`
	Role      MsgRole `json:"role" bson:"role"`
	Content   []Block `json:"content" bson:"content"`
	// Epoch is the lease fencing token of the writer.
	Epoch int64 `json:"epoch" bson:"epoch"`
	// Author is set for user messages.
	Author string `json:"author,omitempty" bson:"author,omitempty"`
}

// PlainText concatenates the text blocks of m.
func (m *Message) PlainText() string {
	s := ""
	for _, b := range m.Content {
		if b.Type == BlockText {
			if s != "" {
				s += "\n"
			}
			s += b.Text
		}
	}
	return s
}

// ToolUses returns the tool_use blocks of m.
func (m *Message) ToolUses() []Block {
	var out []Block
	for _, b := range m.Content {
		if b.Type == BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}
