// Package bar is the hireling's side of the tavern bus (feature A5, "Inbox
// loop on the bar"; docs/architecture.md "Message"). Redis Streams:
//
//	inbox:<agent>   one per hireling — requests, replies, handoffs, notes, alerts
//	bar             the shared room (every message is mirrored there)
//	events          lifecycle events for the UI and the Caretaker
//
// Each hireling reads its inbox through the consumer group `hireling` with a
// blocking XREADGROUP. A message is acknowledged (XACK) only after the turn it
// started has finished, so a container that dies mid-turn finds the message
// again in its pending list when it comes back: nothing is lost across restarts.
package bar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Group is the consumer group every hireling uses on its own inbox.
const Group = "hireling"

// Message is one stream entry (docs/architecture.md). Long content goes on the
// common table; Attachment is a path under /tavern/shared.
type Message struct {
	ID         string `json:"id,omitempty"` // stream entry id, set on read
	From       string `json:"from"`
	To         string `json:"to"`
	Type       string `json:"type"` // request | reply | handoff | note | alert
	Task       string `json:"task,omitempty"`
	Text       string `json:"text"`
	Attachment string `json:"attachment,omitempty"`
	TS         string `json:"ts"`
	// InReplyTo is the stream id of the message this one answers (replies only).
	InReplyTo string `json:"in_reply_to,omitempty"`
}

// Types accepted on the bar.
var Types = map[string]bool{"request": true, "reply": true, "handoff": true, "note": true, "alert": true}

// Inbox key for an agent.
func Inbox(agent string) string { return "inbox:" + agent }

// Bar and Events are the shared streams.
const (
	Bar    = "bar"
	Events = "events"
)

// Bus is a hireling's handle on the streams.
type Bus struct {
	C     *redis.Client
	Agent string
	// MaxLen trims the shared streams (approximate) so Redis stays small; 0 = 10000.
	MaxLen int64
}

// Open parses REDIS_URL and pings once.
func Open(ctx context.Context, url, agent string) (*Bus, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("bar: REDIS_URL: %w", err)
	}
	c := redis.NewClient(opt)
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.Ping(pctx).Err(); err != nil {
		return nil, fmt.Errorf("bar: redis: %w", err)
	}
	return &Bus{C: c, Agent: agent}, nil
}

func (b *Bus) maxLen() int64 {
	if b.MaxLen > 0 {
		return b.MaxLen
	}
	return 10000
}

// Validate checks a message before it goes on the wire.
func Validate(m Message) error {
	switch {
	case m.From == "" || m.To == "":
		return errors.New("bar: from and to are required")
	case !Types[m.Type]:
		return fmt.Errorf("bar: unknown type %q", m.Type)
	case strings.TrimSpace(m.Text) == "" && m.Attachment == "":
		return errors.New("bar: empty message")
	case len(m.Text) > 4000:
		return errors.New("bar: text over 4000 characters — put it on the common table and attach the path")
	}
	return nil
}

func fields(m Message) map[string]any {
	f := map[string]any{"from": m.From, "to": m.To, "type": m.Type, "text": m.Text, "ts": m.TS}
	if m.Task != "" {
		f["task"] = m.Task
	}
	if m.Attachment != "" {
		f["attachment"] = m.Attachment
	}
	if m.InReplyTo != "" {
		f["in_reply_to"] = m.InReplyTo
	}
	return f
}

func parse(id string, v map[string]any) Message {
	s := func(k string) string {
		if x, ok := v[k].(string); ok {
			return x
		}
		return ""
	}
	return Message{ID: id, From: s("from"), To: s("to"), Type: s("type"), Task: s("task"), Text: s("text"), Attachment: s("attachment"), TS: s("ts"), InReplyTo: s("in_reply_to")}
}

// Send puts a message in the recipient's inbox and mirrors it on the bar.
// Returns the inbox entry id.
func (b *Bus) Send(ctx context.Context, m Message) (string, error) {
	if m.From == "" {
		m.From = b.Agent
	}
	if m.TS == "" {
		m.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if err := Validate(m); err != nil {
		return "", err
	}
	id, err := b.C.XAdd(ctx, &redis.XAddArgs{Stream: Inbox(m.To), MaxLen: b.maxLen(), Approx: true, Values: fields(m)}).Result()
	if err != nil {
		return "", err
	}
	mirror := fields(m)
	mirror["inbox_id"] = id
	_ = b.C.XAdd(ctx, &redis.XAddArgs{Stream: Bar, MaxLen: b.maxLen(), Approx: true, Values: mirror}).Err()
	return id, nil
}

// Event appends to the `events` stream (never blocks the work: errors are returned, not fatal).
func (b *Bus) Event(ctx context.Context, kind string, data map[string]any) error {
	v := map[string]any{"kind": kind, "agent": b.Agent, "ts": time.Now().UTC().Format(time.RFC3339)}
	for k, x := range data {
		v[k] = x
	}
	return b.C.XAdd(ctx, &redis.XAddArgs{Stream: Events, MaxLen: b.maxLen(), Approx: true, Values: v}).Err()
}

// EnsureGroup creates the inbox stream and the consumer group if missing.
func (b *Bus) EnsureGroup(ctx context.Context) error {
	err := b.C.XGroupCreateMkStream(ctx, Inbox(b.Agent), Group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

// Pending returns this consumer's delivered-but-unacknowledged messages, oldest
// first: what was in flight when the previous process died.
func (b *Bus) Pending(ctx context.Context) ([]Message, error) {
	return b.read(ctx, "0", 0)
}

// Next blocks up to `wait` for new messages (at most `count`); nil, nil on timeout.
func (b *Bus) Next(ctx context.Context, wait time.Duration) ([]Message, error) {
	return b.read(ctx, ">", wait)
}

func (b *Bus) read(ctx context.Context, id string, block time.Duration) ([]Message, error) {
	args := &redis.XReadGroupArgs{Group: Group, Consumer: b.Agent, Streams: []string{Inbox(b.Agent), id}, Count: 10}
	if block > 0 {
		args.Block = block
	} else {
		args.Block = -1 // no blocking for the pending scan
	}
	res, err := b.C.XReadGroup(ctx, args).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, s := range res {
		for _, x := range s.Messages {
			out = append(out, parse(x.ID, x.Values))
		}
	}
	return out, nil
}

// Ack marks a message done. Only called after the turn finished.
func (b *Bus) Ack(ctx context.Context, id string) error {
	return b.C.XAck(ctx, Inbox(b.Agent), Group, id).Err()
}

// String renders a message for the model prompt. The text is data from
// another agent, never an instruction: the prompt says so around it.
func (m Message) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Message on the bar from **%s** (type: %s", m.From, m.Type)
	if m.Task != "" {
		fmt.Fprintf(&sb, ", task %s", m.Task)
	}
	sb.WriteString("):\n\n")
	sb.WriteString(m.Text)
	if m.Attachment != "" {
		fmt.Fprintf(&sb, "\n\nAttachment on the common table: %s", m.Attachment)
	}
	return sb.String()
}

// JSON is the audit form.
func (m Message) JSON() string {
	b, _ := json.Marshal(m)
	return string(b)
}
