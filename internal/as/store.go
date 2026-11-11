package as

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// store is the Redis persistence layer. Redis 4.0 is the minimum; newer
// servers are accepted. Values are JSON strings plus sets and sorted sets.
// Atomic rules run in Lua. Commands stay inside the Redis 4.0 set: no streams,
// no ZPOPMIN, no GETDEL, no RESP3, and no CLIENT SETINFO.
type store struct {
	rdb    *redis.Client
	prefix string
}

func openStore(ctx context.Context, cfg Config) (*store, string, error) {
	if err := validatePrefix(cfg.KeyPrefix); err != nil {
		return nil, "", err
	}
	if cfg.RedisDB < 0 || cfg.RedisDB > 15 {
		return nil, "", errors.New("REDIS_DB must be between 0 and 15")
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:            cfg.RedisAddr,
		Password:        cfg.RedisPassword,
		DB:              cfg.RedisDB,
		Protocol:        2,
		DisableIdentity: true,
	})
	info, err := rdb.Info(ctx, "server").Result()
	if err != nil {
		rdb.Close()
		return nil, "", err
	}
	version, err := parseRedisVersion(info)
	if err != nil {
		rdb.Close()
		return nil, "", err
	}
	return &store{rdb: rdb, prefix: cfg.KeyPrefix}, version, nil
}

func (st *store) close() error {
	if st == nil || st.rdb == nil {
		return nil
	}
	err := st.rdb.Close()
	st.rdb = nil
	return err
}

func validatePrefix(p string) error {
	if p == "" || len(p) > 80 || strings.HasSuffix(p, ":") {
		return errors.New("REDIS_PREFIX is invalid")
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == ':', r == '_', r == '-':
		default:
			return errors.New("REDIS_PREFIX is invalid")
		}
	}
	return nil
}

func parseRedisVersion(info string) (string, error) {
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		const prefix = "redis_version:"
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		raw := strings.TrimPrefix(line, prefix)
		parts := strings.Split(raw, ".")
		if len(parts) < 2 {
			return raw, fmt.Errorf("AgentSpace requires Redis 4.0 or newer, connected server is %s", raw)
		}
		major, err1 := strconv.Atoi(parts[0])
		_, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil || major < 4 {
			return raw, fmt.Errorf("AgentSpace requires Redis 4.0 or newer, connected server is %s", raw)
		}
		return raw, nil
	}
	return "", errors.New("redis INFO did not include redis_version")
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o700)
}

func (st *store) key(parts ...string) string {
	return st.prefix + ":" + strings.Join(parts, ":")
}

func (st *store) deletePrefix(ctx context.Context) error {
	if st == nil || st.rdb == nil {
		return nil
	}
	match := st.prefix + ":*"
	for pass := 0; pass < 20; pass++ {
		var cursor uint64
		deleted := 0
		for {
			keys, next, err := st.rdb.Scan(ctx, cursor, match, 200).Result()
			if err != nil {
				return err
			}
			if len(keys) > 0 {
				if err := st.rdb.Del(ctx, keys...).Err(); err != nil {
					return err
				}
				deleted += len(keys)
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
		if deleted == 0 {
			return nil
		}
	}
	return errors.New("could not finish deleting the redis prefix")
}

func stamp() (string, string) {
	t := time.Now().UTC()
	return t.Format("2006-01-02T15:04:05.000Z"), strconv.FormatInt(t.UnixMilli(), 10)
}

func scoreOf(ts string) string {
	t, err := time.Parse("2006-01-02T15:04:05.000Z", ts)
	if err != nil {
		_, score := stamp()
		return score
	}
	return strconv.FormatInt(t.UnixMilli(), 10)
}

func encodeJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (st *store) getString(ctx context.Context, key string) (string, bool, error) {
	v, err := st.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (st *store) getJSON(ctx context.Context, key string, dest any) (bool, error) {
	raw, ok, err := st.getString(ctx, key)
	if err != nil || !ok {
		return ok, err
	}
	if err := json.Unmarshal([]byte(raw), dest); err != nil {
		return false, err
	}
	return true, nil
}

func asString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case []byte:
		return string(t), true
	default:
		return "", false
	}
}

func (st *store) run(ctx context.Context, script *redis.Script, args ...any) ([]any, error) {
	v, err := script.Run(ctx, st.rdb, []string{}, args...).Result()
	if err != nil {
		return nil, err
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("redis script returned %T", v)
	}
	return arr, nil
}

func strAt(arr []any, i int) string {
	if i < 0 || i >= len(arr) || arr[i] == nil {
		return ""
	}
	if s, ok := asString(arr[i]); ok {
		return s
	}
	return fmt.Sprint(arr[i])
}

type userRecord struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	CreatedAt    string `json:"created_at"`
}

type sessionRecord struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	ExpiresAt string `json:"expires_at"`
}

// Field order is part of the Redis contract. updated_at is first so Lua can
// touch it without parsing the description, and owner_id stays ahead of any
// text the user typed.
type spaceRecord struct {
	UpdatedAt   string `json:"updated_at"`
	ID          string `json:"id"`
	OwnerID     string `json:"owner_id"`
	CreatedAt   string `json:"created_at"`
	Name        string `json:"name"`
	Goal        string `json:"goal"`
	RunRef      string `json:"run_ref"`
	Description string `json:"description"`
}

func (sp spaceRecord) space() Space {
	return Space{
		ID: sp.ID, Name: sp.Name, Goal: sp.Goal, Description: sp.Description, RunRef: sp.RunRef,
		OwnerID: sp.OwnerID, CreatedAt: sp.CreatedAt, UpdatedAt: sp.UpdatedAt,
	}
}

// status and revoked_at stay near the front so Lua can rewrite them with a first-match scan.
type inviteRecord struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	RevokedAt string `json:"revoked_at"`
	SpaceID   string `json:"space_id"`
	CreatedBy string `json:"created_by"`
	CodeHash  string `json:"code_hash"`
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
}

func (inv inviteRecord) invitation() Invitation {
	out := Invitation{ID: inv.ID, SpaceID: inv.SpaceID, Status: inv.Status, CreatedBy: inv.CreatedBy, CreatedAt: inv.CreatedAt}
	if inv.ExpiresAt != "" {
		expires := inv.ExpiresAt
		out.ExpiresAt = &expires
	}
	if inv.RevokedAt != "" {
		revoked := inv.RevokedAt
		out.RevokedAt = &revoked
	}
	return out
}

type agentRecord struct {
	ID          string `json:"id"`
	CreatedAt   string `json:"created_at"`
	InstanceID  string `json:"instance_id"`
	DisplayName string `json:"display_name"`
	Runtime     string `json:"runtime"`
	OwnerLabel  string `json:"owner_label"`
	Description string `json:"description"`
}

type memberRecord struct {
	JoinedAt     string `json:"joined_at"`
	SpaceID      string `json:"space_id"`
	AgentID      string `json:"agent_id"`
	Status       string `json:"status"`
	TokenHash    string `json:"token_hash"`
	RevokedAt    string `json:"revoked_at"`
	InvitationID string `json:"invitation_id"`
	InvitedBy    string `json:"invited_by"`
}

// revision and space_id stay in front of title and content so Lua can read
// them with a first-match scan. User text cannot shadow those fields.
type docRecord struct {
	UpdatedAt string `json:"updated_at"`
	Revision  int    `json:"revision"`
	ID        string `json:"id"`
	SpaceID   string `json:"space_id"`
	Pinned    bool   `json:"pinned"`
	CreatedBy string `json:"created_by"`
	UpdatedBy string `json:"updated_by"`
	CreatedAt string `json:"created_at"`
	Title     string `json:"title"`
	ContentMD string `json:"content_md"`
}

func (d docRecord) api() Document {
	return Document{
		ID: d.ID, SpaceID: d.SpaceID, Title: d.Title, ContentMD: d.ContentMD, Revision: d.Revision,
		Pinned: d.Pinned, CreatedBy: d.CreatedBy, UpdatedBy: d.UpdatedBy, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

type revRecord struct {
	DocumentID string `json:"document_id"`
	Revision   int    `json:"revision"`
	Title      string `json:"title"`
	ContentMD  string `json:"content_md"`
	Pinned     bool   `json:"pinned"`
	EditorID   string `json:"editor_id"`
	ChangeNote string `json:"change_note"`
	CreatedAt  string `json:"created_at"`
}

func (r revRecord) api() Revision {
	return Revision{
		DocumentID: r.DocumentID, Revision: r.Revision, Title: r.Title, ContentMD: r.ContentMD,
		Pinned: r.Pinned, EditorID: r.EditorID, ChangeNote: r.ChangeNote, CreatedAt: r.CreatedAt,
	}
}

type assetRecord struct {
	ID         string `json:"id"`
	SpaceID    string `json:"space_id"`
	Filename   string `json:"filename"`
	MimeType   string `json:"mime_type"`
	Size       int64  `json:"size"`
	StorageKey string `json:"storage_key"`
	UploadedBy string `json:"uploaded_by"`
	CreatedAt  string `json:"created_at"`
	DeletedAt  string `json:"deleted_at"`
}

type roomRecord struct {
	ID        string   `json:"id"`
	SpaceID   string   `json:"space_id"`
	Type      string   `json:"type"`
	DMKey     string   `json:"dm_key,omitempty"`
	MemberIDs []string `json:"member_ids"`
	CreatedAt string   `json:"created_at"`
}

func (r roomRecord) api() Room {
	ids := r.MemberIDs
	if ids == nil {
		ids = []string{}
	}
	return Room{ID: r.ID, SpaceID: r.SpaceID, Type: r.Type, MemberIDs: ids, CreatedAt: r.CreatedAt}
}

// room_id is the first field so a reply check never matches text inside the body.
type messageRecord struct {
	RoomID           string   `json:"room_id"`
	ID               string   `json:"id"`
	SenderID         string   `json:"sender_id"`
	ReplyToMessageID string   `json:"reply_to_message_id,omitempty"`
	IdempotencyKey   string   `json:"idempotency_key,omitempty"`
	Mentions         []string `json:"mentions"`
	DocumentRefs     []DocRef `json:"document_refs"`
	AssetIDs         []string `json:"asset_ids"`
	CreatedAt        string   `json:"created_at"`
	ContentMD        string   `json:"content_md"`
}

func (m messageRecord) message() Message {
	out := Message{
		ID: m.ID, RoomID: m.RoomID, SenderID: m.SenderID, ContentMD: m.ContentMD,
		ReplyToMessageID: m.ReplyToMessageID, Mentions: m.Mentions, DocumentRefs: m.DocumentRefs,
		AssetIDs: m.AssetIDs, CreatedAt: m.CreatedAt, IdempotencyKey: m.IdempotencyKey,
	}
	if out.Mentions == nil {
		out.Mentions = []string{}
	}
	if out.DocumentRefs == nil {
		out.DocumentRefs = []DocRef{}
	}
	if out.AssetIDs == nil {
		out.AssetIDs = []string{}
	}
	return out
}

type storedEvent struct {
	Seq        int64  `json:"seq"`
	Type       string `json:"type"`
	ResourceID string `json:"resource_id"`
	ActorID    string `json:"actor_id"`
	RoomID     string `json:"room_id,omitempty"`
	Revision   int    `json:"revision,omitempty"`
	Summary    string `json:"summary,omitempty"`
	CreatedAt  string `json:"created_at"`
	Visibility string `json:"visibility"`
	SpaceID    string `json:"space_id"`
}

func (e storedEvent) event() Event {
	return Event{
		Seq: e.Seq, Type: e.Type, ResourceID: e.ResourceID, ActorID: e.ActorID, RoomID: e.RoomID,
		Revision: e.Revision, Summary: e.Summary, CreatedAt: e.CreatedAt, Visibility: e.Visibility, SpaceID: e.SpaceID,
	}
}

func eventJSON(space, ts string, in newEvent) (string, error) {
	raw, err := json.Marshal(storedEvent{
		Type: in.Type, ResourceID: in.ResourceID, ActorID: in.ActorID, RoomID: in.RoomID,
		Revision: in.Revision, Summary: trimSummary(in.Summary), CreatedAt: ts, Visibility: in.Visibility, SpaceID: space,
	})
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(string(raw), `{"seq":0`) {
		return "", errors.New("event json must start with seq")
	}
	return string(raw), nil
}

type auditRecord struct {
	ID         string `json:"id"`
	SpaceID    string `json:"space_id"`
	ActorID    string `json:"actor_id"`
	Action     string `json:"action"`
	ObjectType string `json:"object_type"`
	ObjectID   string `json:"object_id"`
	Result     string `json:"result"`
	CreatedAt  string `json:"created_at"`
}

func newAudit(space, actor, action, objectType, objectID, result, ts string) auditRecord {
	if actor == "" {
		actor = "anonymous"
	}
	if result == "" {
		result = "ok"
	}
	return auditRecord{
		ID: newID("aud_"), SpaceID: space, ActorID: actor, Action: action,
		ObjectType: objectType, ObjectID: objectID, Result: result, CreatedAt: ts,
	}
}

func (st *store) hasAdmin(ctx context.Context) (bool, error) {
	n, err := st.rdb.Exists(ctx, st.key("admin", "ready")).Result()
	return n > 0, err
}

func (st *store) userByName(ctx context.Context, username string) (userRecord, bool, error) {
	id, ok, err := st.getString(ctx, st.key("user", "name", username))
	if err != nil || !ok {
		return userRecord{}, false, err
	}
	var user userRecord
	ok, err = st.getJSON(ctx, st.key("user", "id", id), &user)
	return user, ok, err
}

func (st *store) userByID(ctx context.Context, id string) (userRecord, bool, error) {
	var user userRecord
	ok, err := st.getJSON(ctx, st.key("user", "id", id), &user)
	return user, ok, err
}

func (st *store) putSession(ctx context.Context, tokenHash string, rec sessionRecord, ttl time.Duration) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return st.rdb.Set(ctx, st.key("session", tokenHash), raw, ttl).Err()
}

func (st *store) getSession(ctx context.Context, tokenHash string) (sessionRecord, bool, error) {
	var rec sessionRecord
	ok, err := st.getJSON(ctx, st.key("session", tokenHash), &rec)
	if err != nil || !ok {
		return sessionRecord{}, false, err
	}
	if rec.ExpiresAt < now() {
		_ = st.rdb.Del(ctx, st.key("session", tokenHash)).Err()
		return sessionRecord{}, false, nil
	}
	return rec, true, nil
}

func (st *store) deleteSession(ctx context.Context, tokenHash string) error {
	return st.rdb.Del(ctx, st.key("session", tokenHash)).Err()
}

func (st *store) addAudit(ctx context.Context, rec auditRecord) error {
	if rec.ID == "" {
		rec.ID = newID("aud_")
	}
	if rec.CreatedAt == "" {
		rec.CreatedAt = now()
	}
	if rec.ActorID == "" {
		rec.ActorID = "anonymous"
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := st.rdb.Set(ctx, st.key("audit", rec.ID), raw, 0).Err(); err != nil {
		return err
	}
	if rec.SpaceID == "" {
		return nil
	}
	score := float64(time.Now().UTC().UnixMilli())
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", rec.CreatedAt); err == nil {
		score = float64(t.UnixMilli())
	}
	return st.rdb.ZAdd(ctx, st.key("space", rec.SpaceID, "audit"), redis.Z{Score: score, Member: rec.ID}).Err()
}

func (st *store) listAudit(ctx context.Context, space string, limit int) ([]AuditEntry, error) {
	ids, err := st.rdb.ZRevRange(ctx, st.key("space", space, "audit"), 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]AuditEntry, 0, len(ids))
	for _, id := range ids {
		var rec auditRecord
		ok, err := st.getJSON(ctx, st.key("audit", id), &rec)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		entry := AuditEntry{
			ID: rec.ID, ActorID: rec.ActorID, Action: rec.Action, ObjectType: rec.ObjectType,
			ObjectID: rec.ObjectID, Result: rec.Result, CreatedAt: rec.CreatedAt,
		}
		if rec.SpaceID != "" {
			spaceID := rec.SpaceID
			entry.SpaceID = &spaceID
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (st *store) getSpace(ctx context.Context, id string) (spaceRecord, bool, error) {
	var sp spaceRecord
	ok, err := st.getJSON(ctx, st.key("space", id), &sp)
	return sp, ok, err
}

func (st *store) listSpaces(ctx context.Context, owner string) ([]spaceRecord, error) {
	ids, err := st.rdb.ZRevRange(ctx, st.key("owner", owner, "spaces"), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]spaceRecord, 0, len(ids))
	for _, id := range ids {
		sp, ok, err := st.getSpace(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok && sp.OwnerID == owner {
			out = append(out, sp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (st *store) saveSpace(ctx context.Context, sp spaceRecord) error {
	raw, err := json.Marshal(sp)
	if err != nil {
		return err
	}
	score := float64(time.Now().UTC().UnixMilli())
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", sp.UpdatedAt); err == nil {
		score = float64(t.UnixMilli())
	}
	_, err = st.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Set(ctx, st.key("space", sp.ID), raw, 0)
		p.ZAdd(ctx, st.key("owner", sp.OwnerID, "spaces"), redis.Z{Score: score, Member: sp.ID})
		return nil
	})
	return err
}

type spaceStats struct {
	GeneralRoomID string
	MemberCount   int
	DocumentCount int
	ServerSeq     int64
}

func (st *store) stats(ctx context.Context, space string) (spaceStats, error) {
	general, ok, err := st.getString(ctx, st.key("space", space, "general"))
	if err != nil {
		return spaceStats{}, err
	}
	if !ok {
		return spaceStats{}, notFound("general room not found")
	}
	members, err := st.rdb.ZCard(ctx, st.key("space", space, "members")).Result()
	if err != nil {
		return spaceStats{}, err
	}
	docs, err := st.rdb.SCard(ctx, st.key("space", space, "docs")).Result()
	if err != nil {
		return spaceStats{}, err
	}
	seq, _, err := st.serverSeq(ctx, space)
	if err != nil {
		return spaceStats{}, err
	}
	return spaceStats{GeneralRoomID: general, MemberCount: int(members), DocumentCount: int(docs), ServerSeq: seq}, nil
}

func (st *store) getInvite(ctx context.Context, id string) (inviteRecord, bool, error) {
	var inv inviteRecord
	ok, err := st.getJSON(ctx, st.key("invite", id), &inv)
	return inv, ok, err
}

func (st *store) inviteOwner(ctx context.Context, inv inviteRecord) (string, error) {
	if inv.CreatedBy != "" {
		return inv.CreatedBy, nil
	}
	sp, ok, err := st.getSpace(ctx, inv.SpaceID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	return sp.OwnerID, nil
}

func (st *store) activeInviteForUser(ctx context.Context, space, userID string) (inviteRecord, bool, error) {
	if userID == "" {
		return inviteRecord{}, false, nil
	}
	id, ok, err := st.getString(ctx, st.key("space", space, "active_invite", userID))
	if err != nil {
		return inviteRecord{}, false, err
	}
	if !ok {
		id, ok, err = st.getString(ctx, st.key("space", space, "active_invite"))
		if err != nil || !ok {
			return inviteRecord{}, false, err
		}
		inv, found, err := st.getInvite(ctx, id)
		if err != nil || !found || inv.Status != "active" || inv.SpaceID != space {
			return inviteRecord{}, false, err
		}
		owner, err := st.inviteOwner(ctx, inv)
		if err != nil || owner != userID {
			return inviteRecord{}, false, err
		}
		return inv, true, nil
	}
	inv, found, err := st.getInvite(ctx, id)
	if err != nil || !found || inv.Status != "active" || inv.SpaceID != space {
		return inviteRecord{}, false, err
	}
	if inv.CreatedBy != "" && inv.CreatedBy != userID {
		return inviteRecord{}, false, nil
	}
	return inv, true, nil
}

func (st *store) listInvites(ctx context.Context, space string) ([]inviteRecord, error) {
	ids, err := st.rdb.ZRevRange(ctx, st.key("space", space, "invites"), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]inviteRecord, 0, len(ids))
	for _, id := range ids {
		inv, ok, err := st.getInvite(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok && inv.SpaceID == space {
			out = append(out, inv)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (st *store) inviteByHash(ctx context.Context, codeHash string) (inviteRecord, bool, error) {
	id, ok, err := st.getString(ctx, st.key("invite", "code", codeHash))
	if err != nil || !ok {
		return inviteRecord{}, false, err
	}
	return st.getInvite(ctx, id)
}

func (st *store) lookupToken(ctx context.Context, tokenHash string) (string, string, string, bool, error) {
	raw, ok, err := st.getString(ctx, st.key("token", tokenHash))
	if err != nil || !ok {
		return "", "", "", false, err
	}
	spaceID, agentID, found := strings.Cut(raw, "|")
	if !found || spaceID == "" || agentID == "" {
		return "", "", "", false, nil
	}
	var member memberRecord
	ok, err = st.getJSON(ctx, st.key("member", spaceID, agentID), &member)
	if err != nil || !ok || member.Status != "active" || member.TokenHash != tokenHash {
		return "", "", "", false, err
	}
	return spaceID, agentID, member.Status, true, nil
}

func (st *store) touchSeen(ctx context.Context, agentID, ts string) error {
	return st.rdb.Set(ctx, st.key("seen", agentID), ts, 0).Err()
}

func (st *store) agentName(ctx context.Context, id string) (string, error) {
	var agent agentRecord
	ok, err := st.getJSON(ctx, st.key("agent", id), &agent)
	if err != nil || !ok || agent.DisplayName == "" {
		return id, err
	}
	return agent.DisplayName, nil
}

func (st *store) memberStatus(ctx context.Context, space, agent string) (string, bool, error) {
	var member memberRecord
	ok, err := st.getJSON(ctx, st.key("member", space, agent), &member)
	if err != nil || !ok {
		return "", false, err
	}
	return member.Status, true, nil
}

func (st *store) activeAgentIDs(ctx context.Context, space string) ([]string, error) {
	ids, err := st.rdb.ZRange(ctx, st.key("space", space, "members"), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		status, ok, err := st.memberStatus(ctx, space, id)
		if err != nil {
			return nil, err
		}
		if ok && status == "active" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (st *store) members(ctx context.Context, space string, limit int) ([]Member, error) {
	ids, err := st.rdb.ZRange(ctx, st.key("space", space, "members"), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]Member, 0, len(ids))
	for _, id := range ids {
		var member memberRecord
		ok, err := st.getJSON(ctx, st.key("member", space, id), &member)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var agent agentRecord
		ok, err = st.getJSON(ctx, st.key("agent", id), &agent)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		item := Member{
			AgentID: agent.ID, DisplayName: agent.DisplayName, Runtime: agent.Runtime, InstanceID: agent.InstanceID,
			OwnerLabel: agent.OwnerLabel, Description: agent.Description, JoinedAt: member.JoinedAt, Status: member.Status,
			InvitationID: member.InvitationID, InvitedBy: member.InvitedBy,
		}
		if seen, ok, err := st.getString(ctx, st.key("seen", id)); err != nil {
			return nil, err
		} else if ok && seen != "" {
			value := seen
			item.LastSeenAt = &value
		}
		out = append(out, item)
	}
	if err := st.attachInviterNames(ctx, out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].JoinedAt != out[j].JoinedAt {
			return out[i].JoinedAt < out[j].JoinedAt
		}
		return out[i].AgentID < out[j].AgentID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (st *store) attachInviterNames(ctx context.Context, members []Member) error {
	seen := map[string]string{}
	for i, m := range members {
		if m.InvitedBy == "" {
			continue
		}
		if name, ok := seen[m.InvitedBy]; ok {
			members[i].InvitedByUsername = name
			continue
		}
		user, ok, err := st.userByID(ctx, m.InvitedBy)
		if err != nil {
			return err
		}
		name := ""
		if ok {
			name = user.Username
		}
		seen[m.InvitedBy] = name
		members[i].InvitedByUsername = name
	}
	return nil
}

func (st *store) getDoc(ctx context.Context, space, id string, allowDeleted bool) (docRecord, error) {
	var doc docRecord
	ok, err := st.getJSON(ctx, st.key("doc", id), &doc)
	if err != nil {
		return docRecord{}, err
	}
	if !ok || doc.SpaceID != space {
		return docRecord{}, notFound("document not found")
	}
	n, err := st.rdb.Exists(ctx, st.key("doc", id, "deleted")).Result()
	if err != nil {
		return docRecord{}, err
	}
	if n > 0 && !allowDeleted {
		return docRecord{}, notFound("document not found")
	}
	return doc, nil
}

func (st *store) listDocs(ctx context.Context, space string) ([]docRecord, error) {
	ids, err := st.rdb.SMembers(ctx, st.key("space", space, "docs")).Result()
	if err != nil || len(ids) == 0 {
		return []docRecord{}, err
	}
	keys := make([]string, len(ids))
	deleted := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = st.key("doc", id)
		deleted[i] = st.key("doc", id, "deleted")
	}
	vals, err := st.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	flags, err := st.rdb.MGet(ctx, deleted...).Result()
	if err != nil {
		return nil, err
	}
	out := make([]docRecord, 0, len(ids))
	for i, v := range vals {
		if v == nil || flags[i] != nil {
			continue
		}
		raw, ok := asString(v)
		if !ok {
			continue
		}
		var doc docRecord
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			return nil, err
		}
		if doc.SpaceID == space {
			out = append(out, doc)
		}
	}
	return out, nil
}

func (st *store) listRevs(ctx context.Context, docID string) ([]revRecord, error) {
	nums, err := st.rdb.SMembers(ctx, st.key("doc", docID, "revs")).Result()
	if err != nil {
		return nil, err
	}
	revs := make([]revRecord, 0, len(nums))
	for _, num := range nums {
		n, err := strconv.Atoi(num)
		if err != nil {
			continue
		}
		rev, ok, err := st.getRev(ctx, docID, n)
		if err != nil {
			return nil, err
		}
		if ok {
			revs = append(revs, rev)
		}
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i].Revision > revs[j].Revision })
	return revs, nil
}

func (st *store) getRev(ctx context.Context, docID string, n int) (revRecord, bool, error) {
	var rev revRecord
	ok, err := st.getJSON(ctx, st.key("doc", docID, "rev", strconv.Itoa(n)), &rev)
	return rev, ok, err
}

func (st *store) getAsset(ctx context.Context, space, id string) (assetRecord, error) {
	var asset assetRecord
	ok, err := st.getJSON(ctx, st.key("asset", id), &asset)
	if err != nil {
		return assetRecord{}, err
	}
	if !ok || asset.SpaceID != space || asset.DeletedAt != "" {
		return assetRecord{}, notFound("asset not found")
	}
	return asset, nil
}

func (st *store) assetLive(ctx context.Context, space, id string) (bool, error) {
	_, err := st.getAsset(ctx, space, id)
	if isCode(err, CodeNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (st *store) getRoom(ctx context.Context, space, id string) (roomRecord, error) {
	var room roomRecord
	ok, err := st.getJSON(ctx, st.key("room", id), &room)
	if err != nil {
		return roomRecord{}, err
	}
	if !ok || room.SpaceID != space {
		return roomRecord{}, notFound("room not found")
	}
	if room.MemberIDs == nil {
		room.MemberIDs = []string{}
	}
	if room.Type == "general" {
		ids, err := st.activeAgentIDs(ctx, space)
		if err != nil {
			return roomRecord{}, err
		}
		room.MemberIDs = ids
	}
	return room, nil
}

func (st *store) listRooms(ctx context.Context, space, agent string, admin bool) ([]roomRecord, error) {
	generalID, ok, err := st.getString(ctx, st.key("space", space, "general"))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFound("general room not found")
	}
	ids := []string{generalID}
	if !admin {
		extra, err := st.rdb.SMembers(ctx, st.key("agent", agent, "rooms", space)).Result()
		if err != nil {
			return nil, err
		}
		ids = append(ids, extra...)
	}
	out := make([]roomRecord, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		room, err := st.getRoom(ctx, space, id)
		if err != nil {
			return nil, err
		}
		out = append(out, room)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (st *store) messageInRoom(ctx context.Context, room, id string) (bool, error) {
	var msg messageRecord
	ok, err := st.getJSON(ctx, st.key("msg", id), &msg)
	if err != nil || !ok {
		return false, err
	}
	return msg.RoomID == room, nil
}

func (st *store) messageByID(ctx context.Context, id string) (messageRecord, bool, error) {
	var msg messageRecord
	ok, err := st.getJSON(ctx, st.key("msg", id), &msg)
	return msg, ok, err
}

func (st *store) messageByIdem(ctx context.Context, room, sender, key string) (messageRecord, bool, error) {
	id, ok, err := st.getString(ctx, st.key("idem", room, sender, key))
	if err != nil || !ok {
		return messageRecord{}, false, err
	}
	return st.messageByID(ctx, id)
}

func (st *store) listMessages(ctx context.Context, room, q, cursor string, limit int) ([]messageRecord, string, bool, error) {
	max := "+inf"
	if cursor != "" {
		parts, err := decodeCursor(cursor)
		if err != nil || len(parts) != 1 {
			return nil, "", false, badRequest("invalid cursor")
		}
		n, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || n < 0 {
			return nil, "", false, badRequest("invalid cursor")
		}
		max = "(" + parts[0]
	}
	out := make([]messageRecord, 0, limit)
	scores := make([]int64, 0, limit)
	scanned := 0
	for len(out) <= limit && scanned < 5000 {
		count := int64(200)
		if q == "" {
			count = int64(limit + 1 - len(out))
		}
		batch, err := st.rdb.ZRevRangeByScoreWithScores(ctx, st.key("room", room, "msgs"), &redis.ZRangeBy{
			Min: "-inf", Max: max, Count: count,
		}).Result()
		if err != nil {
			return nil, "", false, err
		}
		if len(batch) == 0 {
			break
		}
		for _, z := range batch {
			scanned++
			id, ok := asString(z.Member)
			if !ok {
				id = fmt.Sprint(z.Member)
			}
			msg, ok, err := st.messageByID(ctx, id)
			if err != nil {
				return nil, "", false, err
			}
			if ok && (q == "" || containsFold(msg.ContentMD, q)) {
				out = append(out, msg)
				scores = append(scores, int64(z.Score))
			}
			if len(out) > limit {
				break
			}
		}
		max = "(" + strconv.FormatInt(int64(batch[len(batch)-1].Score), 10)
		if len(batch) < int(count) || q == "" {
			break
		}
	}
	hasMore := len(out) > limit
	next := ""
	if hasMore {
		out = out[:limit]
		scores = scores[:limit]
		next = encodeCursor(strconv.FormatInt(scores[len(scores)-1], 10))
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, next, hasMore, nil
}

func (st *store) serverSeq(ctx context.Context, space string) (int64, bool, error) {
	n, err := st.rdb.Exists(ctx, st.key("space", space)).Result()
	if err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, false, nil
	}
	v, err := st.rdb.Get(ctx, st.key("space", space, "seq")).Result()
	if errors.Is(err, redis.Nil) {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	seq, err := strconv.ParseInt(v, 10, 64)
	return seq, true, err
}

func (st *store) eventsAfter(ctx context.Context, space string, after int64, limit int) ([]Event, int, int64, error) {
	opt := &redis.ZRangeBy{Min: "(" + strconv.FormatInt(after, 10), Max: "+inf"}
	if limit > 0 {
		opt.Count = int64(limit)
	}
	seqs, err := st.rdb.ZRangeByScore(ctx, st.key("events", space), opt).Result()
	if err != nil {
		return nil, 0, after, err
	}
	if len(seqs) == 0 {
		return nil, 0, after, nil
	}
	keys := make([]string, len(seqs))
	for i, seq := range seqs {
		keys[i] = st.key("event", space, seq)
	}
	vals, err := st.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, 0, after, err
	}
	out := make([]Event, 0, len(seqs))
	for _, v := range vals {
		raw, ok := asString(v)
		if !ok {
			continue
		}
		ev, err := decodeEvent(raw)
		if err != nil {
			return nil, 0, after, err
		}
		out = append(out, ev)
	}
	last, err := strconv.ParseInt(seqs[len(seqs)-1], 10, 64)
	if err != nil {
		return nil, 0, after, err
	}
	return out, len(seqs), last, nil
}

func (st *store) hasEventsAfter(ctx context.Context, space string, seq int64) (bool, error) {
	n, err := st.rdb.ZCount(ctx, st.key("events", space), "("+strconv.FormatInt(seq, 10), "+inf").Result()
	return n > 0, err
}

func (st *store) cursor(ctx context.Context, space, agent string) (int64, error) {
	v, ok, err := st.getString(ctx, st.key("cursor", space, agent))
	if err != nil || !ok || v == "" {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

func (st *store) visibleRooms(ctx context.Context, space, agent string, admin bool) (map[string]bool, error) {
	general, ok, err := st.getString(ctx, st.key("space", space, "general"))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFound("general room not found")
	}
	set := map[string]bool{general: true}
	if admin {
		return set, nil
	}
	ids, err := st.rdb.SMembers(ctx, st.key("agent", agent, "rooms", space)).Result()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

func decodeEvent(raw string) (Event, error) {
	var stored storedEvent
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return Event{}, err
	}
	return stored.event(), nil
}
