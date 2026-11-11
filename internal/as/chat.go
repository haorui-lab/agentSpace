package as

import (
	"context"
	"sort"
	"strings"
)

const maxMessageBytes = 100_000

type SendMessageInput struct {
	ContentMD        string
	Mentions         []string
	DocumentRefs     []DocRef
	AssetIDs         []string
	IdempotencyKey   string
	ReplyToMessageID string
}

func (s *Server) ListRooms(ctx context.Context, p Principal, spaceID string) ([]Room, error) {
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return nil, err
	}
	rows, err := s.st.listRooms(ctx, spaceID, p.AgentID, p.Kind == KindAdmin)
	if err != nil {
		return nil, err
	}
	out := make([]Room, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.api())
	}
	return out, nil
}

func (s *Server) GeneralRoom(ctx context.Context, p Principal, spaceID string) (Room, error) {
	rooms, err := s.ListRooms(ctx, p, spaceID)
	if err != nil {
		return Room{}, err
	}
	for _, room := range rooms {
		if room.Type == "general" {
			return room, nil
		}
	}
	return Room{}, notFound("general room not found")
}

func (s *Server) OpenDM(ctx context.Context, p Principal, spaceID, peerID string) (Room, error) {
	if p.Kind != KindAgent {
		return Room{}, forbidden("only agent members can open a direct conversation")
	}
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return Room{}, err
	}
	if peerID == "" || peerID == p.AgentID {
		return Room{}, badRequest("peer_agent_id must be another member")
	}
	if err := s.requireActiveMember(ctx, spaceID, p.AgentID); err != nil {
		return Room{}, err
	}
	if err := s.requireActiveMember(ctx, spaceID, peerID); err != nil {
		return Room{}, err
	}
	room, ev, created, err := s.st.openDM(ctx, spaceID, p.AgentID, peerID, newID("room_"))
	if err != nil {
		return Room{}, err
	}
	if created {
		s.hub.publish(ev)
	}
	return room.api(), nil
}

func dmKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + ":" + b
}

func (s *Server) requireActiveMember(ctx context.Context, spaceID, agentID string) error {
	status, ok, err := s.st.memberStatus(ctx, spaceID, agentID)
	if err != nil {
		return err
	}
	if !ok || status != "active" {
		return notFound("member not found")
	}
	return nil
}

func (s *Server) loadRoom(ctx context.Context, spaceID, roomID string) (Room, error) {
	room, err := s.st.getRoom(ctx, spaceID, roomID)
	if err != nil {
		return Room{}, err
	}
	return room.api(), nil
}

func (s *Server) authorizeRoom(ctx context.Context, p Principal, room Room) error {
	if err := s.authorizeSpace(ctx, p, room.SpaceID); err != nil {
		return err
	}
	if room.Type == "general" {
		if p.Kind == KindAdmin {
			return nil
		}
		return s.requireActiveMember(ctx, room.SpaceID, p.AgentID)
	}
	if p.Kind != KindAgent {
		return forbidden("direct conversations are visible only to the participants")
	}
	for _, id := range room.MemberIDs {
		if id == p.AgentID {
			return s.requireActiveMember(ctx, room.SpaceID, p.AgentID)
		}
	}
	return forbidden("direct conversations are visible only to the participants")
}

type MessagePage struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"next_cursor,omitempty"`
	HasMore    bool      `json:"has_more"`
}

func (s *Server) ListMessages(ctx context.Context, p Principal, spaceID, roomID, q, cursor string, limit int) (MessagePage, error) {
	room, err := s.loadRoom(ctx, spaceID, roomID)
	if err != nil {
		return MessagePage{}, err
	}
	if err := s.authorizeRoom(ctx, p, room); err != nil {
		return MessagePage{}, err
	}
	q = strings.TrimSpace(q)
	if len([]rune(q)) > 200 {
		return MessagePage{}, badRequest("query is too long")
	}
	limit = clampLimit(limit, 50, 100)
	rows, next, hasMore, err := s.st.listMessages(ctx, roomID, q, cursor, limit)
	if err != nil {
		return MessagePage{}, err
	}
	page := MessagePage{Messages: make([]Message, 0, len(rows)), NextCursor: next, HasMore: hasMore}
	for _, row := range rows {
		msg := row.message()
		sort.Strings(msg.Mentions)
		msg.SenderDisplayName = s.displayName(ctx, msg.SenderID)
		page.Messages = append(page.Messages, msg)
	}
	return page, nil
}

func (s *Server) displayName(ctx context.Context, id string) string {
	if strings.HasPrefix(id, "usr_") {
		return "管理员"
	}
	name, err := s.st.agentName(ctx, id)
	if err != nil || name == "" {
		return id
	}
	return name
}

func (s *Server) SendMessage(ctx context.Context, p Principal, spaceID, roomID string, in SendMessageInput) (Message, error) {
	room, err := s.loadRoom(ctx, spaceID, roomID)
	if err != nil {
		return Message{}, err
	}
	if err := s.authorizeRoom(ctx, p, room); err != nil {
		return Message{}, err
	}
	content := in.ContentMD
	if strings.Contains(content, "\x00") || len(content) > maxMessageBytes {
		return Message{}, badRequest("content_md is invalid")
	}
	if strings.TrimSpace(content) == "" && len(in.AssetIDs) == 0 {
		return Message{}, badRequest("message content or an attachment is required")
	}
	key := strings.TrimSpace(in.IdempotencyKey)
	if len(key) > 200 {
		return Message{}, badRequest("idempotency_key is too long")
	}
	mentions := uniqueStrings(in.Mentions)
	sort.Strings(mentions)
	for _, id := range mentions {
		if err := s.requireActiveMember(ctx, spaceID, id); err != nil {
			return Message{}, badRequest("mention is not an active member")
		}
	}
	refs := make([]DocRef, 0, len(in.DocumentRefs))
	for _, ref := range in.DocumentRefs {
		resolved, err := s.resolveDocRef(ctx, spaceID, ref)
		if err != nil {
			return Message{}, err
		}
		refs = append(refs, resolved)
	}
	assets := uniqueStrings(in.AssetIDs)
	for _, id := range assets {
		if err := s.requireAsset(ctx, spaceID, id); err != nil {
			return Message{}, err
		}
	}
	if in.ReplyToMessageID != "" {
		ok, err := s.st.messageInRoom(ctx, roomID, in.ReplyToMessageID)
		if err != nil {
			return Message{}, err
		}
		if !ok {
			return Message{}, badRequest("reply_to_message_id is not in this room")
		}
	}
	if key != "" {
		existing, ok, err := s.st.messageByIdem(ctx, roomID, p.ActorID(), key)
		if err != nil {
			return Message{}, err
		}
		if ok {
			return s.finishReplay(ctx, existing, content, in.ReplyToMessageID, mentions, refs, assets)
		}
	}
	msg := messageRecord{
		RoomID: roomID, ID: newID("msg_"), SenderID: p.ActorID(), ReplyToMessageID: in.ReplyToMessageID,
		IdempotencyKey: key, Mentions: mentions, DocumentRefs: refs, AssetIDs: assets, CreatedAt: now(), ContentMD: content,
	}
	vis := "space"
	if room.Type == "dm" {
		vis = "room"
	}
	ev, replayID, err := s.st.sendMessage(ctx, spaceID, msg, vis)
	if err != nil {
		return Message{}, err
	}
	if replayID != "" {
		existing, ok, err := s.st.messageByID(ctx, replayID)
		if err != nil {
			return Message{}, err
		}
		if !ok {
			return Message{}, conflict("idempotency key reused with a different payload", nil)
		}
		return s.finishReplay(ctx, existing, content, in.ReplyToMessageID, mentions, refs, assets)
	}
	s.hub.publish(ev)
	out := msg.message()
	out.SenderDisplayName = s.displayName(ctx, out.SenderID)
	sort.Strings(out.Mentions)
	return out, nil
}

func (s *Server) finishReplay(ctx context.Context, existing messageRecord, content, reply string, mentions []string, refs []DocRef, assets []string) (Message, error) {
	msg := existing.message()
	sort.Strings(msg.Mentions)
	msg.SenderDisplayName = s.displayName(ctx, msg.SenderID)
	if samePayload(msg, content, reply, mentions, refs, assets) {
		msg.Replayed = true
		return msg, nil
	}
	return Message{}, conflict("idempotency key reused with a different payload", map[string]any{"message_id": msg.ID})
}

func samePayload(existing Message, content, reply string, mentions []string, refs []DocRef, assets []string) bool {
	if existing.ContentMD != content || existing.ReplyToMessageID != reply {
		return false
	}
	if !sameSet(existing.Mentions, mentions) || !sameSet(existing.AssetIDs, assets) {
		return false
	}
	if len(existing.DocumentRefs) != len(refs) {
		return false
	}
	left := append([]DocRef(nil), existing.DocumentRefs...)
	right := append([]DocRef(nil), refs...)
	sort.Slice(left, func(i, j int) bool {
		return left[i].DocumentID < left[j].DocumentID || (left[i].DocumentID == left[j].DocumentID && left[i].Revision < left[j].Revision)
	})
	sort.Slice(right, func(i, j int) bool {
		return right[i].DocumentID < right[j].DocumentID || (right[i].DocumentID == right[j].DocumentID && right[i].Revision < right[j].Revision)
	})
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func sameSet(a, b []string) bool {
	aa := uniqueStrings(a)
	bb := uniqueStrings(b)
	sort.Strings(aa)
	sort.Strings(bb)
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func (s *Server) resolveDocRef(ctx context.Context, spaceID string, ref DocRef) (DocRef, error) {
	if ref.DocumentID == "" {
		return DocRef{}, badRequest("document_refs.document_id is required")
	}
	doc, err := s.st.getDoc(ctx, spaceID, ref.DocumentID, false)
	if err != nil {
		if isCode(err, CodeNotFound) {
			return DocRef{}, badRequest("document reference is not in this space")
		}
		return DocRef{}, err
	}
	rev := ref.Revision
	if rev == 0 {
		rev = doc.Revision
	}
	_, ok, err := s.st.getRev(ctx, ref.DocumentID, rev)
	if err != nil {
		return DocRef{}, err
	}
	if !ok {
		return DocRef{}, badRequest("document revision does not exist")
	}
	return DocRef{DocumentID: ref.DocumentID, Revision: rev}, nil
}

func (s *Server) requireAsset(ctx context.Context, spaceID, assetID string) error {
	ok, err := s.st.assetLive(ctx, spaceID, assetID)
	if err != nil {
		return err
	}
	if !ok {
		return badRequest("asset is not in this space")
	}
	return nil
}
