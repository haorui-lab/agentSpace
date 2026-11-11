package as

import (
	"context"
	"strconv"
	"strings"
)

type newEvent struct {
	Type       string
	ActorID    string
	ResourceID string
	RoomID     string
	Visibility string
	Revision   int
	Summary    string
}

func (s *Server) serverSeq(ctx context.Context, spaceID string) (int64, error) {
	seq, ok, err := s.st.serverSeq(ctx, spaceID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, notFound("space not found")
	}
	return seq, nil
}

func (s *Server) PullEvents(ctx context.Context, p Principal, spaceID string, after int64, limit int) (PullResult, error) {
	if after < 0 {
		return PullResult{}, badRequest("after_seq must be >= 0")
	}
	limit = clampLimit(limit, 100, 200)
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return PullResult{}, err
	}
	serverSeq, err := s.serverSeq(ctx, spaceID)
	if err != nil {
		return PullResult{}, err
	}
	empty := PullResult{Events: []Event{}, NextSeq: after, HasMore: false, ServerSeq: serverSeq}
	if after >= serverSeq {
		return empty, nil
	}
	scanned, n, last, err := s.st.eventsAfter(ctx, spaceID, after, limit)
	if err != nil {
		return PullResult{}, err
	}
	if n == 0 {
		return empty, nil
	}
	hasMore := false
	if n == limit {
		hasMore, err = s.st.hasEventsAfter(ctx, spaceID, last)
		if err != nil {
			return PullResult{}, err
		}
	}
	rooms, err := s.visibleRoomSet(ctx, p, spaceID)
	if err != nil {
		return PullResult{}, err
	}
	visible := make([]Event, 0, len(scanned))
	for _, ev := range scanned {
		if canSeeEvent(p, rooms, ev) {
			ev.Visibility = ""
			ev.SpaceID = ""
			visible = append(visible, ev)
		}
	}
	return PullResult{Events: visible, NextSeq: last, HasMore: hasMore, ServerSeq: serverSeq}, nil
}

func canSeeEvent(p Principal, rooms map[string]bool, ev Event) bool {
	if ev.Visibility != "room" {
		return true
	}
	if p.Kind == KindAdmin {
		return false
	}
	return rooms[ev.RoomID]
}

func (s *Server) visibleRoomSet(ctx context.Context, p Principal, spaceID string) (map[string]bool, error) {
	return s.st.visibleRooms(ctx, spaceID, p.AgentID, p.Kind == KindAdmin)
}

func (s *Server) AckEvents(ctx context.Context, p Principal, spaceID string, last int64) (int64, string, error) {
	if p.Kind != KindAgent {
		return 0, "", forbidden("only an agent member can acknowledge events")
	}
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return 0, "", err
	}
	if last < 0 {
		return 0, "", badRequest("last_ack_seq must be >= 0")
	}
	ts, err := s.st.ack(ctx, spaceID, p.AgentID, last)
	if err != nil {
		return 0, "", err
	}
	return last, ts, nil
}

type Bootstrap struct {
	Space           Space      `json:"space"`
	PinnedDocuments []DocIndex `json:"pinned_documents"`
	RecentDocuments []DocIndex `json:"recent_documents"`
	Members         []Member   `json:"members"`
	GeneralRoom     Room       `json:"general_room"`
	Rooms           []Room     `json:"rooms"`
	EventCursor     CursorInfo `json:"event_cursor"`
}

type CursorInfo struct {
	ServerSeq      int64 `json:"server_seq"`
	LastAckSeq     int64 `json:"last_ack_seq"`
	PendingVisible int   `json:"pending_visible"`
}

func (s *Server) Bootstrap(ctx context.Context, p Principal, spaceID string) (Bootstrap, error) {
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return Bootstrap{}, err
	}
	sp, err := s.loadSpace(ctx, spaceID)
	if err != nil {
		return Bootstrap{}, err
	}
	sp.OwnerID = ""
	pinned, err := s.listDocuments(ctx, spaceID, "", "", 20, true)
	if err != nil {
		return Bootstrap{}, err
	}
	recent, err := s.listDocuments(ctx, spaceID, "", "", 20, false)
	if err != nil {
		return Bootstrap{}, err
	}
	members, err := s.ListMembers(ctx, p, spaceID)
	if err != nil {
		return Bootstrap{}, err
	}
	rooms, err := s.ListRooms(ctx, p, spaceID)
	if err != nil {
		return Bootstrap{}, err
	}
	var general Room
	for _, room := range rooms {
		if room.Type == "general" {
			general = room
			break
		}
	}
	serverSeq, err := s.serverSeq(ctx, spaceID)
	if err != nil {
		return Bootstrap{}, err
	}
	var lastAck int64
	if p.Kind == KindAgent {
		lastAck, err = s.st.cursor(ctx, spaceID, p.AgentID)
		if err != nil {
			return Bootstrap{}, err
		}
	}
	pending, err := s.countVisibleAfter(ctx, p, spaceID, lastAck)
	if err != nil {
		return Bootstrap{}, err
	}
	return Bootstrap{
		Space: sp, PinnedDocuments: pinned, RecentDocuments: recent, Members: members,
		GeneralRoom: general, Rooms: rooms,
		EventCursor: CursorInfo{ServerSeq: serverSeq, LastAckSeq: lastAck, PendingVisible: pending},
	}, nil
}

func (s *Server) countVisibleAfter(ctx context.Context, p Principal, spaceID string, after int64) (int, error) {
	events, _, _, err := s.st.eventsAfter(ctx, spaceID, after, 0)
	if err != nil {
		return 0, err
	}
	rooms, err := s.visibleRoomSet(ctx, p, spaceID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ev := range events {
		if canSeeEvent(p, rooms, ev) {
			n++
		}
	}
	return n, nil
}

func (s *Server) ListAudit(ctx context.Context, p Principal, spaceID string, limit int) ([]AuditEntry, error) {
	if p.Kind != KindAdmin {
		return nil, forbidden("only the space owner can read the audit log")
	}
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return nil, err
	}
	limit = clampLimit(limit, 50, 200)
	return s.st.listAudit(ctx, spaceID, limit)
}

func parseAfter(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, badRequest("after_seq must be a non-negative integer")
	}
	return n, nil
}
