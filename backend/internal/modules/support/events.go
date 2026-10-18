package support

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type attachment struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}
type event struct {
	Provider      string       `json:"provider"`
	ContentDigest string       `json:"contentDigest,omitempty"`
	Timestamp     int64        `json:"timestamp,omitempty"`
	ChatID        int64        `json:"chatId"`
	MessageID     string       `json:"messageId"`
	SenderID      int64        `json:"senderId"`
	Text          string       `json:"text"`
	ThreadID      int64        `json:"threadId"`
	ReplyID       string       `json:"replyId"`
	Private       bool         `json:"private"`
	Anonymous     bool         `json:"anonymous"`
	Bot           bool         `json:"bot"`
	Attachments   []attachment `json:"attachments,omitempty"`
	Unsupported   bool         `json:"unsupported,omitempty"`
	Discovery     *Discovery   `json:"-"`
	Start         bool         `json:"-"`
}

// Discovery deliberately contains no sender identity, title, message, token or attachment.
type Discovery struct {
	Type      string `json:"type"`
	ChatID    int64  `json:"chatId"`
	Timestamp int64  `json:"timestamp"`
}

type telegramMessage struct {
	ID       int64  `json:"message_id"`
	ThreadID int64  `json:"message_thread_id"`
	Text     string `json:"text"`
	Chat     struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	From struct {
		ID  int64 `json:"id"`
		Bot bool  `json:"is_bot"`
	} `json:"from"`
	SenderChat json.RawMessage `json:"sender_chat"`
	Reply      *struct {
		ID int64 `json:"message_id"`
	} `json:"reply_to_message"`
}

func normalize(provider string, body []byte) (*event, string, error) {
	if provider == "telegram" {
		var u struct {
			ID      *int64           `json:"update_id"`
			Message *telegramMessage `json:"message"`
		}
		if err := json.Unmarshal(body, &u); err != nil || u.ID == nil || *u.ID < 0 {
			return nil, "", errors.New("invalid update")
		}
		if u.Message == nil {
			return nil, "", nil
		}
		msg := u.Message
		if msg.ID <= 0 || msg.Chat.ID == 0 {
			return nil, "", errors.New("invalid message")
		}
		e := &event{Provider: provider, ChatID: msg.Chat.ID, MessageID: strconv.FormatInt(msg.ID, 10), SenderID: msg.From.ID, ThreadID: msg.ThreadID, Private: msg.Chat.Type == "private", Anonymous: len(msg.SenderChat) > 0 && string(msg.SenderChat) != "null", Bot: msg.From.Bot}
		fields := strings.Fields(msg.Text)
		e.Start = e.Private && len(fields) > 0 && fields[0] == "/start"
		if msg.Reply != nil {
			e.ReplyID = strconv.FormatInt(msg.Reply.ID, 10)
		}
		e.ContentDigest = messageDigest(body)
		return e, strconv.FormatInt(*u.ID, 10), nil
	}
	var u struct {
		Type      string `json:"update_type"`
		Timestamp int64  `json:"timestamp"`
		ChatID    int64  `json:"chat_id"`
		User      *struct {
			ID  int64 `json:"user_id"`
			Bot bool  `json:"is_bot"`
		} `json:"user"`
		Message *struct {
			Sender struct {
				ID  int64 `json:"user_id"`
				Bot bool  `json:"is_bot"`
			} `json:"sender"`
			Recipient struct {
				ID   int64  `json:"chat_id"`
				Type string `json:"chat_type"`
			} `json:"recipient"`
			Link *struct {
				Type    string `json:"type"`
				Message *struct {
					ID string `json:"mid"`
				} `json:"message"`
			} `json:"link"`
			Body *struct {
				ID          string       `json:"mid"`
				Text        string       `json:"text"`
				Attachments []attachment `json:"attachments"`
			} `json:"body"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &u); err != nil || u.Type == "" {
		return nil, "", errors.New("invalid update")
	}
	if u.Type == "bot_started" {
		if u.ChatID <= 0 || u.User == nil || u.User.ID <= 0 || u.Timestamp <= 0 {
			return nil, "", errors.New("invalid start")
		}
		return &event{Provider: provider, ChatID: u.ChatID, SenderID: u.User.ID, Private: true, Bot: u.User.Bot, Start: true}, "", nil
	}
	if u.Type == "bot_added" || u.Type == "bot_removed" {
		if u.ChatID == 0 || u.Timestamp <= 0 {
			return nil, "", errors.New("invalid discovery")
		}
		return &event{Provider: provider, Discovery: &Discovery{u.Type, u.ChatID, u.Timestamp}}, fmt.Sprintf("discovery:%s:%d:%d", u.Type, u.ChatID, u.Timestamp), nil
	}
	if u.Type != "message_created" {
		return nil, "", nil
	}
	if u.Message == nil || u.Message.Sender.ID <= 0 || u.Message.Recipient.ID == 0 {
		return nil, "", errors.New("invalid message")
	}
	// MAX can represent a forwarded message using link only. It has no body
	// ID to deduplicate/deliver; acknowledge it without treating it as a reply.
	if u.Message.Body == nil {
		if u.Message.Link != nil && u.Message.Link.Type == "forward" {
			return nil, "", nil
		}
		return nil, "", errors.New("invalid message")
	}
	if u.Message.Body.ID == "" || len(u.Message.Body.ID) > 256 {
		return nil, "", errors.New("invalid message")
	}
	msg := u.Message
	e := &event{Provider: provider, ChatID: msg.Recipient.ID, SenderID: msg.Sender.ID, MessageID: msg.Body.ID, Text: msg.Body.Text, Timestamp: u.Timestamp, Private: msg.Recipient.Type == "dialog", Bot: msg.Sender.Bot}
	// Only a reply identifies a customer route; a forwarded operator message never does.
	if msg.Link != nil && msg.Link.Type == "reply" && msg.Link.Message != nil {
		e.ReplyID = msg.Link.Message.ID
	}
	if len(e.ReplyID) > 256 {
		return nil, "", errors.New("invalid reply")
	}
	if len([]rune(e.Text)) > 4000 || len(msg.Body.Attachments) > 12 {
		e.Unsupported = true
	}
	for _, a := range msg.Body.Attachments {
		clean, ok := nativeAttachment(a)
		if !ok {
			e.Unsupported = true
			continue
		}
		e.Attachments = append(e.Attachments, clean)
	}
	if e.Text == "" && len(e.Attachments) == 0 {
		e.Unsupported = true
	}
	e.ContentDigest = messageDigest(body)
	return e, fmt.Sprintf("%d:%s", msg.Recipient.ID, msg.Body.ID), nil
}

// Native MAX attachments are re-created from delivery fields only. Message links,
// recipients, internal chat IDs and other incoming metadata never leave this module.
func nativeAttachment(a attachment) (attachment, bool) {
	var p map[string]json.RawMessage
	if json.Unmarshal(a.Payload, &p) != nil {
		return attachment{}, false
	}
	allowed := map[string]bool{}
	switch a.Type {
	case "image":
		var token string
		if raw, ok := p["token"]; ok && json.Unmarshal(raw, &token) == nil && token != "" && len(token) <= 16384 {
			allowed["token"] = true
		} else {
			allowed["url"] = true
		}
	case "file", "audio", "video":
		allowed["token"] = true
	case "sticker":
		allowed["code"] = true
	default:
		return attachment{}, false
	}
	clean := make(map[string]json.RawMessage)
	for k := range allowed {
		if raw, ok := p[k]; ok {
			var value string
			if json.Unmarshal(raw, &value) != nil || value == "" || len(value) > 16384 {
				return attachment{}, false
			}
			clean[k] = raw
		}
	}
	if len(clean) == 0 {
		return attachment{}, false
	}
	body, err := json.Marshal(clean)
	if err != nil {
		return attachment{}, false
	}
	return attachment{a.Type, body}, true
}

func messageDigest(body []byte) string {
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return ""
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw["message"]))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return ""
	}
	canonical, _ := json.Marshal(value)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
