package stats

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func bsonField(key string, value any) bson.E {
	return bson.E{Key: key, Value: value}
}

// usageEntry is the only stats payload shape. One document represents either
// a command-level anonymous bucket or a concrete (command, Telegram user) pair.
type usageEntry struct {
	Cmd      string `json:"cmd" bson:"cmd"`
	UserID   int64  `json:"uid,omitempty" bson:"uid,omitempty"`
	Username string `json:"user,omitempty" bson:"user,omitempty"`
	N        int64  `json:"n" bson:"n"`
	Deleted  bool   `json:"deleted,omitempty" bson:"deleted,omitempty"`
}

type usageUser struct {
	ID       int64
	Username string
}

type usageStore interface {
	Increment(ctx context.Context, cmd string, user usageUser, hasUser bool) error
	TopCommands(ctx context.Context, limit int) ([]row, error)
	TopUsers(ctx context.Context, limit int) ([]row, error)
	CommandsByUser(ctx context.Context, username string, limit int) ([]row, bool, error)
	UsersByCommand(ctx context.Context, cmd string, limit int) ([]row, error)
}

func newUsageStore(coll storage.Collection) usageStore {
	if mongoColl, ok := storage.MongoCollection(coll); ok {
		return &mongoUsageStore{coll: mongoColl}
	}
	return &docUsageStore{docs: storage.Typed[usageEntry](coll)}
}

// usageKey names a usage row: the bare command for the anonymous bucket (a
// sender without a public username), or "<cmd>:<userID>" for a per-user row.
func usageKey(cmd string, userID int64) string {
	if userID == 0 {
		return cmd
	}
	return cmd + ":" + strconv.FormatInt(userID, 10)
}

// docUsageStore implements usageStore over a plain DocStore, for the in-memory
// provider. Every aggregate reads the whole collection, which is fine at the
// sizes that backend sees. mu serialises the read-modify-write in Increment,
// since Put is unconditional and two concurrent increments would otherwise
// lose one.
type docUsageStore struct {
	mu   sync.Mutex
	docs storage.DocStore[usageEntry]
}

func (s *docUsageStore) Increment(ctx context.Context, cmd string, user usageUser, hasUser bool) error {
	if isRetiredCommand(cmd) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	userID := int64(0)
	if hasUser {
		userID = user.ID
	}
	key := usageKey(cmd, userID)
	entry, _, err := s.docs.Get(ctx, key)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		entry = usageEntry{}
	case err != nil:
		return fmt.Errorf("stats get %s: %w", key, err)
	}

	entry.Cmd = cmd
	entry.N++
	entry.Deleted = false
	if hasUser {
		entry.UserID = user.ID
		entry.Username = user.Username
	} else {
		entry.UserID = 0
		entry.Username = ""
	}
	if err := s.docs.Put(ctx, key, entry); err != nil {
		return fmt.Errorf("stats put %s: %w", key, err)
	}
	if hasUser {
		return s.refreshUsernameLocked(ctx, user)
	}
	return nil
}

// refreshUsernameLocked rewrites the username on every row of user.ID, so a
// Telegram username change carries the user's whole history with it and
// /stats user <new name> finds all of it. It also clears that username from
// other users' rows: Telegram usernames can move to a new account, and a stale
// copy would make lookups by name ambiguous. The caller must hold s.mu.
func (s *docUsageStore) refreshUsernameLocked(ctx context.Context, user usageUser) error {
	entries, err := s.loadEntriesLocked(ctx)
	if err != nil {
		return err
	}
	for _, e := range entries {
		switch {
		case e.UserID == user.ID && e.Username != user.Username:
			e.Username = user.Username
		case e.UserID != user.ID && user.Username != "" && e.Username == user.Username:
			e.Username = ""
		default:
			continue
		}
		if err := s.docs.Put(ctx, usageKey(e.Cmd, e.UserID), e); err != nil {
			return fmt.Errorf("stats refresh username %d: %w", user.ID, err)
		}
	}
	return nil
}

func (s *docUsageStore) TopCommands(ctx context.Context, limit int) ([]row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.loadEntriesLocked(ctx)
	if err != nil {
		return nil, err
	}
	totals := make(map[string]int64)
	for _, e := range entries {
		if e.Deleted || isRetiredCommand(e.Cmd) {
			continue
		}
		totals[e.Cmd] += e.N
	}
	rows := make([]row, 0, len(totals))
	for cmd, n := range totals {
		rows = append(rows, row{display: "/" + cmd, n: n})
	}
	sortRows(rows)
	return limitRows(rows, limit), nil
}

func (s *docUsageStore) TopUsers(ctx context.Context, limit int) ([]row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.loadEntriesLocked(ctx)
	if err != nil {
		return nil, err
	}
	type total struct {
		username string
		n        int64
	}
	totals := make(map[int64]total)
	for _, e := range entries {
		if e.Deleted || isRetiredCommand(e.Cmd) || e.UserID == 0 || e.Username == "" {
			continue
		}
		t := totals[e.UserID]
		t.username = e.Username
		t.n += e.N
		totals[e.UserID] = t
	}
	rows := make([]row, 0, len(totals))
	for _, t := range totals {
		rows = append(rows, row{display: "@" + t.username, n: t.n})
	}
	sortRows(rows)
	return limitRows(rows, limit), nil
}

func (s *docUsageStore) CommandsByUser(ctx context.Context, username string, limit int) ([]row, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.loadEntriesLocked(ctx)
	if err != nil {
		return nil, false, err
	}
	var (
		userID int64
		found  bool
	)
	for _, e := range entries {
		if !e.Deleted && !isRetiredCommand(e.Cmd) && e.UserID != 0 && e.Username == username {
			userID = e.UserID
			found = true
			break
		}
	}
	if !found {
		return nil, false, nil
	}

	rows := make([]row, 0)
	for _, e := range entries {
		if !e.Deleted && !isRetiredCommand(e.Cmd) && e.UserID == userID {
			rows = append(rows, row{display: "/" + e.Cmd, n: e.N})
		}
	}
	sortRows(rows)
	return limitRows(rows, limit), true, nil
}

func (s *docUsageStore) UsersByCommand(ctx context.Context, cmd string, limit int) ([]row, error) {
	if isRetiredCommand(cmd) {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.loadEntriesLocked(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]row, 0)
	for _, e := range entries {
		if !e.Deleted && e.Cmd == cmd && e.UserID != 0 && e.Username != "" {
			rows = append(rows, row{display: "@" + e.Username, n: e.N})
		}
	}
	sortRows(rows)
	return limitRows(rows, limit), nil
}

func (s *docUsageStore) loadEntriesLocked(ctx context.Context) ([]usageEntry, error) {
	keys, err := s.docs.List(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("stats list: %w", err)
	}
	entries := make([]usageEntry, 0, len(keys))
	for _, key := range keys {
		entry, _, err := s.docs.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("stats get %s: %w", key, err)
		}
		if entry.Cmd == "" {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// mongoUsageStore implements usageStore with native MongoDB updates and
// aggregations, backed by the indexes ensureUsageIndexes creates.
type mongoUsageStore struct {
	coll *mongo.Collection
}

// Increment upserts the row with $inc, so concurrent increments need no lock.
// Unsetting "deleted" revives a soft-deleted row that is being used again; the
// retired command is filtered out before that can happen. A follow-up
// UpdateMany propagates a username change to the user's other rows, and a
// second one clears the username from any other account that held it before.
func (s *mongoUsageStore) Increment(ctx context.Context, cmd string, user usageUser, hasUser bool) error {
	if isRetiredCommand(cmd) {
		return nil
	}
	userID := int64(0)
	if hasUser {
		userID = user.ID
	}

	set := bson.M{"cmd": cmd}
	unset := bson.M{"deleted": ""}
	update := bson.M{
		"$set":         set,
		"$inc":         bson.M{"n": int64(1), "version": int64(1)},
		"$unset":       unset,
		"$currentDate": bson.M{"updatedAt": true},
	}
	if hasUser {
		set["uid"] = user.ID
		set["user"] = user.Username
	} else {
		unset["uid"] = ""
		unset["user"] = ""
	}

	key := usageKey(cmd, userID)
	if _, err := s.coll.UpdateOne(ctx,
		bson.M{"_id": key},
		update,
		options.UpdateOne().SetUpsert(true),
	); err != nil {
		return fmt.Errorf("mongo stats increment %s: %w", key, err)
	}
	if !hasUser {
		return nil
	}
	if _, err := s.coll.UpdateMany(ctx,
		bson.M{"uid": user.ID, "user": bson.M{"$ne": user.Username}},
		bson.M{
			"$set":         bson.M{"user": user.Username},
			"$inc":         bson.M{"version": int64(1)},
			"$currentDate": bson.M{"updatedAt": true},
		},
	); err != nil {
		return fmt.Errorf("mongo stats refresh username %d: %w", user.ID, err)
	}
	if user.Username == "" {
		return nil
	}
	// The username may have moved from another account; drop the stale copy so
	// lookups by name resolve to one user.
	if _, err := s.coll.UpdateMany(ctx,
		bson.M{"uid": bson.M{"$ne": user.ID}, "user": user.Username},
		bson.M{
			"$unset":       bson.M{"user": ""},
			"$inc":         bson.M{"version": int64(1)},
			"$currentDate": bson.M{"updatedAt": true},
		},
	); err != nil {
		return fmt.Errorf("mongo stats clear moved username %s: %w", user.Username, err)
	}
	return nil
}

func (s *mongoUsageStore) TopCommands(ctx context.Context, limit int) ([]row, error) {
	pipeline := mongo.Pipeline{
		bson.D{bsonField("$match", bson.D{
			bsonField("cmd", bson.D{bsonField("$type", "string"), bsonField("$ne", deletedStockDividendCommand)}),
			bsonField("deleted", bson.D{bsonField("$ne", true)}),
		})},
		bson.D{bsonField("$group", bson.D{bsonField("_id", "$cmd"), bsonField("n", bson.D{bsonField("$sum", "$n")})})},
		bson.D{bsonField("$sort", bson.D{bsonField("n", -1), bsonField("_id", 1)})},
	}
	pipeline = withLimit(pipeline, limit)

	cur, err := s.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("mongo stats top commands: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	var rows []row
	for cur.Next(ctx) {
		var doc struct {
			Cmd string `bson:"_id"`
			N   int64  `bson:"n"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("mongo stats top commands decode: %w", err)
		}
		rows = append(rows, row{display: "/" + doc.Cmd, n: doc.N})
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("mongo stats top commands cursor: %w", err)
	}
	return rows, nil
}

func (s *mongoUsageStore) TopUsers(ctx context.Context, limit int) ([]row, error) {
	pipeline := mongo.Pipeline{
		bson.D{bsonField("$match", bson.D{
			bsonField("cmd", bson.D{bsonField("$ne", deletedStockDividendCommand)}),
			bsonField("uid", bson.D{bsonField("$gt", int64(0))}),
			bsonField("user", bson.D{bsonField("$type", "string"), bsonField("$ne", "")}),
			bsonField("deleted", bson.D{bsonField("$ne", true)}),
		})},
		// Newest row first, so $first labels each user with their most recent
		// username.
		bson.D{bsonField("$sort", bson.D{bsonField("updatedAt", -1)})},
		bson.D{bsonField("$group", bson.D{
			bsonField("_id", "$uid"),
			bsonField("user", bson.D{bsonField("$first", "$user")}),
			bsonField("n", bson.D{bsonField("$sum", "$n")}),
		})},
		bson.D{bsonField("$sort", bson.D{bsonField("n", -1), bsonField("user", 1)})},
	}
	pipeline = withLimit(pipeline, limit)

	cur, err := s.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("mongo stats top users: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	var rows []row
	for cur.Next(ctx) {
		var doc struct {
			User string `bson:"user"`
			N    int64  `bson:"n"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("mongo stats top users decode: %w", err)
		}
		rows = append(rows, row{display: "@" + doc.User, n: doc.N})
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("mongo stats top users cursor: %w", err)
	}
	return rows, nil
}

func (s *mongoUsageStore) CommandsByUser(ctx context.Context, username string, limit int) ([]row, bool, error) {
	userID, found, err := s.userIDByUsername(ctx, username)
	if err != nil || !found {
		return nil, found, err
	}

	opts := options.Find().
		SetProjection(bson.M{"cmd": 1, "n": 1}).
		SetSort(bson.D{bsonField("n", -1), bsonField("cmd", 1)})
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	cur, err := s.coll.Find(ctx, bson.M{
		"uid":     userID,
		"cmd":     bson.M{"$type": "string", "$ne": deletedStockDividendCommand},
		"deleted": bson.M{"$ne": true},
	}, opts)
	if err != nil {
		return nil, true, fmt.Errorf("mongo stats commands by user %s: %w", username, err)
	}
	defer func() { _ = cur.Close(ctx) }()

	var rows []row
	for cur.Next(ctx) {
		var doc usageEntry
		if err := cur.Decode(&doc); err != nil {
			return nil, true, fmt.Errorf("mongo stats commands by user decode: %w", err)
		}
		rows = append(rows, row{display: "/" + doc.Cmd, n: doc.N})
	}
	if err := cur.Err(); err != nil {
		return nil, true, fmt.Errorf("mongo stats commands by user cursor: %w", err)
	}
	return rows, true, nil
}

func (s *mongoUsageStore) UsersByCommand(ctx context.Context, cmd string, limit int) ([]row, error) {
	if isRetiredCommand(cmd) {
		return nil, nil
	}
	opts := options.Find().
		SetProjection(bson.M{"user": 1, "n": 1}).
		SetSort(bson.D{bsonField("n", -1), bsonField("user", 1)})
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	cur, err := s.coll.Find(ctx, bson.M{
		"cmd":     cmd,
		"uid":     bson.M{"$gt": int64(0)},
		"user":    bson.M{"$type": "string", "$ne": ""},
		"deleted": bson.M{"$ne": true},
	}, opts)
	if err != nil {
		return nil, fmt.Errorf("mongo stats users by command %s: %w", cmd, err)
	}
	defer func() { _ = cur.Close(ctx) }()

	var rows []row
	for cur.Next(ctx) {
		var doc usageEntry
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("mongo stats users by command decode: %w", err)
		}
		rows = append(rows, row{display: "@" + doc.Username, n: doc.N})
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("mongo stats users by command cursor: %w", err)
	}
	return rows, nil
}

func (s *mongoUsageStore) userIDByUsername(ctx context.Context, username string) (int64, bool, error) {
	var doc struct {
		UserID int64 `bson:"uid"`
	}
	err := s.coll.FindOne(ctx,
		bson.M{
			"cmd":     bson.M{"$ne": deletedStockDividendCommand},
			"uid":     bson.M{"$gt": int64(0)},
			"user":    username,
			"deleted": bson.M{"$ne": true},
		},
		options.FindOne().
			SetProjection(bson.M{"uid": 1}),
	).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("mongo stats find user %s: %w", username, err)
	}
	return doc.UserID, true, nil
}

// isRetiredCommand reports whether cmd has been removed from the bot. Its rows
// are never incremented or shown, even ones written before the migration
// marked them deleted.
func isRetiredCommand(cmd string) bool {
	return cmd == deletedStockDividendCommand
}

func withLimit(pipeline mongo.Pipeline, limit int) mongo.Pipeline {
	if limit <= 0 {
		return pipeline
	}
	return append(pipeline, bson.D{bsonField("$limit", int64(limit))})
}

func limitRows(rows []row, limit int) []row {
	if limit <= 0 || len(rows) <= limit {
		return rows
	}
	return rows[:limit]
}
