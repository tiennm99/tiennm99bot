// Package subscription holds the opt-in list behind a module's daily push:
// the stored (chat, topic) subscribers, the once-per-day claim that keeps a
// push idempotent, and the fan-out that sends to every subscriber and prunes
// chats that can no longer be reached.
package subscription

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// Key is the store slot holding a module's subscriber list.
const Key = "subscribers"

// Subscriber is one row in the subscriber list. ThreadID is the Telegram
// forum-topic id the user subscribed from; 0 means the chat's General topic
// (or a non-forum chat). Uniqueness key is (ChatID, ThreadID) so the same
// chat can subscribe independently in multiple topics.
//
// Telegram routes outgoing messages with an absent/zero message_thread_id
// to the General topic, so carrying ThreadID alongside ChatID is what keeps
// the daily push landing in the topic the user subscribed from.
type Subscriber struct {
	ChatID   int64 `json:"chat_id" bson:"chat_id"`
	ThreadID int   `json:"thread_id,omitempty" bson:"thread_id,omitempty"`
}

// Doc wraps the subscriber list so it can be stored as a named root field in
// a Mongo document (a bare JSON array cannot be a root doc).
type Doc struct {
	Subscribers []Subscriber `json:"subscribers" bson:"subscribers"`
}

// Store is the typed store for subscriber documents.
type Store = storage.DocStore[Doc]

// List returns the current subscriber list, or nil if none have ever
// subscribed.
func List(ctx context.Context, store Store) ([]Subscriber, error) {
	doc, _, err := store.Get(ctx, Key)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("subscription list: %w", err)
	}
	return doc.Subscribers, nil
}

// Add appends (chatID, threadID) if that exact pair is absent. Returns true
// on first add, false when already subscribed (idempotent).
//
// Concurrency: the list lives in a single store slot, so a concurrent
// Get→mutate→Put from two chats subscribing in the same millisecond would
// drop one write. Callers MUST serialize through a module-scoped mutex (the
// same one passed to Fanout) before calling this.
func Add(ctx context.Context, store Store, chatID int64, threadID int) (bool, error) {
	subs, err := List(ctx, store)
	if err != nil {
		return false, err
	}
	for _, s := range subs {
		if s.ChatID == chatID && s.ThreadID == threadID {
			return false, nil
		}
	}
	subs = append(subs, Subscriber{ChatID: chatID, ThreadID: threadID})
	if err := store.Put(ctx, Key, Doc{Subscribers: subs}); err != nil {
		return false, fmt.Errorf("subscription add: %w", err)
	}
	return true, nil
}

// Remove drops the single (chatID, threadID) entry. Returns true when
// removed, false when that exact pair wasn't present (idempotent).
//
// Concurrency: same single-slot Get→mutate→Put as Add; callers must hold the
// module's subscriber mutex.
func Remove(ctx context.Context, store Store, chatID int64, threadID int) (bool, error) {
	n, err := removeWhere(ctx, store, func(s Subscriber) bool {
		return s.ChatID == chatID && s.ThreadID == threadID
	})
	return n > 0, err
}

// RemoveAllForChat drops every entry for chatID regardless of ThreadID.
// Used when a send fails with a chat-wide terminal error (bot blocked, chat
// deactivated, kicked, deleted) — every topic subscription in that chat is
// dead, not just the one the failing send targeted. Returns the number of
// entries actually removed.
//
// Concurrency: callers must hold the module's subscriber mutex.
func RemoveAllForChat(ctx context.Context, store Store, chatID int64) (int, error) {
	return removeWhere(ctx, store, func(s Subscriber) bool { return s.ChatID == chatID })
}

func removeWhere(ctx context.Context, store Store, drop func(Subscriber) bool) (int, error) {
	subs, err := List(ctx, store)
	if err != nil {
		return 0, err
	}
	out := make([]Subscriber, 0, len(subs))
	for _, s := range subs {
		if !drop(s) {
			out = append(out, s)
		}
	}
	removed := len(subs) - len(out)
	if removed == 0 {
		return 0, nil
	}
	if err := store.Put(ctx, Key, Doc{Subscribers: out}); err != nil {
		return 0, fmt.Errorf("subscription remove: %w", err)
	}
	return removed, nil
}

// Scope names where a subscription applies, for use mid-sentence:
// "this topic" inside a forum topic, otherwise "this chat".
func Scope(msg *models.Message) string {
	if msg != nil && msg.MessageThreadID != 0 {
		return "this topic"
	}
	return "this chat"
}

// ScopeSubject is Scope capitalized for the start of a sentence.
func ScopeSubject(msg *models.Message) string {
	if msg != nil && msg.MessageThreadID != 0 {
		return "This topic"
	}
	return "This chat"
}
