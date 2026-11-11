package as

import (
	"context"
	"net/url"
	"strings"
	"time"
)

type CreateSpaceInput struct {
	Name                string
	Goal                string
	Description         string
	RunRef              string
	InvitationExpiresAt string
}

type CreateSpaceResult struct {
	Space         Space      `json:"space"`
	Invitation    Invitation `json:"invitation"`
	GeneralRoomID string     `json:"general_room_id"`
}

func (s *Server) CreateSpace(ctx context.Context, ownerID string, in CreateSpaceInput, base string) (CreateSpaceResult, error) {
	name, ok := trimMax(in.Name, 120)
	if !ok {
		return CreateSpaceResult{}, badRequest("name is required and must be at most 120 characters")
	}
	goal, ok := trimMaxAllowEmpty(in.Goal, 4000)
	if !ok {
		return CreateSpaceResult{}, badRequest("goal is too long or contains invalid characters")
	}
	description := strings.TrimSpace(in.Description)
	if strings.Contains(description, "\x00") || len(description) > 8000 {
		return CreateSpaceResult{}, badRequest("description is invalid")
	}
	runRef, ok := trimMaxAllowEmpty(in.RunRef, 200)
	if !ok {
		return CreateSpaceResult{}, badRequest("run_ref is invalid")
	}
	expires, err := parseExpiry(in.InvitationExpiresAt)
	if err != nil {
		return CreateSpaceResult{}, err
	}
	ts := now()
	sp := spaceRecord{
		UpdatedAt: ts, ID: newID("sp_"), OwnerID: ownerID, CreatedAt: ts,
		Name: name, Goal: goal, RunRef: runRef, Description: description,
	}
	room := roomRecord{ID: newID("room_"), SpaceID: sp.ID, Type: "general", MemberIDs: []string{}, CreatedAt: ts}
	code := newSecret("ic_")
	inv := inviteRecord{
		ID: newID("inv_"), Status: "active", SpaceID: sp.ID, CreatedBy: ownerID,
		CodeHash: hashSecret(code), Code: code, CreatedAt: ts,
	}
	if expires != nil {
		inv.ExpiresAt = *expires
	}
	if err := s.st.createSpace(ctx, sp, room, inv, ownerID); err != nil {
		return CreateSpaceResult{}, err
	}
	apiSpace := sp.space()
	apiSpace.OwnerID = ""
	apiInvite := inv.invitation()
	apiInvite.Code = code
	apiInvite.URL = inviteURL(base, code)
	return CreateSpaceResult{Space: apiSpace, Invitation: apiInvite, GeneralRoomID: room.ID}, nil
}

func inviteURL(base, code string) string {
	return strings.TrimRight(base, "/") + "/i/" + url.PathEscape(code)
}

func parseExpiry(raw string) (*string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, badRequest("invitation_expires_at must be RFC3339 UTC")
	}
	formatted := t.UTC().Format("2006-01-02T15:04:05.000Z")
	return &formatted, nil
}

func (s *Server) ListSpaces(ctx context.Context, ownerID string) ([]Space, error) {
	rows, err := s.st.listSpaces(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	out := make([]Space, 0, len(rows))
	for _, row := range rows {
		sp := row.space()
		sp.OwnerID = ""
		out = append(out, sp)
	}
	return out, nil
}

type SpaceDetail struct {
	Space            Space       `json:"space"`
	GeneralRoomID    string      `json:"general_room_id"`
	ActiveInvitation *Invitation `json:"active_invitation"`
	MemberCount      int         `json:"member_count"`
	DocumentCount    int         `json:"document_count"`
	ServerSeq        int64       `json:"server_seq"`
}

func (s *Server) GetSpace(ctx context.Context, p Principal, spaceID, base string) (SpaceDetail, error) {
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return SpaceDetail{}, err
	}
	sp, err := s.loadSpace(ctx, spaceID)
	if err != nil {
		return SpaceDetail{}, err
	}
	sp.OwnerID = ""
	stats, err := s.st.stats(ctx, spaceID)
	if err != nil {
		return SpaceDetail{}, err
	}
	detail := SpaceDetail{
		Space: sp, GeneralRoomID: stats.GeneralRoomID, MemberCount: stats.MemberCount,
		DocumentCount: stats.DocumentCount, ServerSeq: stats.ServerSeq,
	}
	if p.Kind == KindAdmin {
		inv, err := s.ensureUserInvite(ctx, p, spaceID, base)
		if err != nil {
			return SpaceDetail{}, err
		}
		inv.SpaceID = ""
		detail.ActiveInvitation = &inv
	}
	return detail, nil
}

func (s *Server) loadSpace(ctx context.Context, spaceID string) (Space, error) {
	sp, ok, err := s.st.getSpace(ctx, spaceID)
	if err != nil {
		return Space{}, err
	}
	if !ok {
		return Space{}, notFound("space not found")
	}
	return sp.space(), nil
}

type PatchSpaceInput struct {
	Name        *string
	Goal        *string
	Description *string
	RunRef      *string
}

func (s *Server) PatchSpace(ctx context.Context, p Principal, spaceID string, in PatchSpaceInput) (Space, error) {
	if p.Kind != KindAdmin {
		return Space{}, forbidden("only the space owner can change space settings")
	}
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return Space{}, err
	}
	sp, ok, err := s.st.getSpace(ctx, spaceID)
	if err != nil {
		return Space{}, err
	}
	if !ok {
		return Space{}, notFound("space not found")
	}
	if in.Name != nil {
		name, ok := trimMax(*in.Name, 120)
		if !ok {
			return Space{}, badRequest("name is required and must be at most 120 characters")
		}
		sp.Name = name
	}
	if in.Goal != nil {
		goal, ok := trimMaxAllowEmpty(*in.Goal, 4000)
		if !ok {
			return Space{}, badRequest("goal is invalid")
		}
		sp.Goal = goal
	}
	if in.Description != nil {
		description := strings.TrimSpace(*in.Description)
		if strings.Contains(description, "\x00") || len(description) > 8000 {
			return Space{}, badRequest("description is invalid")
		}
		sp.Description = description
	}
	if in.RunRef != nil {
		runRef, ok := trimMaxAllowEmpty(*in.RunRef, 200)
		if !ok {
			return Space{}, badRequest("run_ref is invalid")
		}
		sp.RunRef = runRef
	}
	sp.UpdatedAt = now()
	if err := s.st.saveSpace(ctx, sp); err != nil {
		return Space{}, err
	}
	_ = s.audit(ctx, spaceID, p.ActorID(), "space.update", "space", spaceID, "ok")
	api := sp.space()
	api.OwnerID = ""
	return api, nil
}

func (s *Server) ListInvitations(ctx context.Context, p Principal, spaceID, base string) ([]Invitation, error) {
	if p.Kind != KindAdmin {
		return nil, forbidden("only the space owner can list invitations")
	}
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return nil, err
	}
	rows, err := s.st.listInvites(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	out := make([]Invitation, 0, len(rows))
	for _, row := range rows {
		api := row.invitation()
		api.SpaceID = ""
		if row.CreatedBy == p.UserID && row.Status == "active" && row.Code != "" {
			api.Code = row.Code
			api.URL = inviteURL(base, row.Code)
		}
		out = append(out, api)
	}
	return out, nil
}

func (s *Server) CreateInvitation(ctx context.Context, p Principal, spaceID, expiresAt, base string) (Invitation, bool, error) {
	if p.Kind != KindAdmin {
		return Invitation{}, false, forbidden("only the space owner can create invitations")
	}
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return Invitation{}, false, err
	}
	active, ok, err := s.st.activeInviteForUser(ctx, spaceID, p.UserID)
	if err != nil {
		return Invitation{}, false, err
	}
	if ok && inviteUsable(active) == nil && active.Code != "" {
		api := s.revealInvite(active, base)
		api.SpaceID = ""
		return api, false, nil
	}
	inv, err := s.issueInvitation(ctx, p, spaceID, expiresAt, base, "invitation.create", ok)
	if err != nil {
		return Invitation{}, false, err
	}
	return inv, true, nil
}

func (s *Server) RotateInvitation(ctx context.Context, p Principal, spaceID, expiresAt, base string) (Invitation, error) {
	if p.Kind != KindAdmin {
		return Invitation{}, forbidden("only the space owner can rotate invitations")
	}
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return Invitation{}, err
	}
	return s.issueInvitation(ctx, p, spaceID, expiresAt, base, "invitation.rotate", true)
}

func (s *Server) revealInvite(inv inviteRecord, base string) Invitation {
	api := inv.invitation()
	if inv.Code != "" {
		api.Code = inv.Code
		api.URL = inviteURL(base, inv.Code)
	}
	return api
}

func (s *Server) ensureUserInvite(ctx context.Context, p Principal, spaceID, base string) (Invitation, error) {
	active, ok, err := s.st.activeInviteForUser(ctx, spaceID, p.UserID)
	if err != nil {
		return Invitation{}, err
	}
	if ok && inviteUsable(active) == nil && active.Code != "" {
		return s.revealInvite(active, base), nil
	}
	action := "invitation.create"
	if ok {
		action = "invitation.rotate"
	}
	inv, err := s.issueInvitation(ctx, p, spaceID, "", base, action, ok)
	if err != nil && isCode(err, CodeConflict) && !ok {
		existing, found, err2 := s.st.activeInviteForUser(ctx, spaceID, p.UserID)
		if err2 != nil {
			return Invitation{}, err2
		}
		if found && inviteUsable(existing) == nil && existing.Code != "" {
			return s.revealInvite(existing, base), nil
		}
		return s.issueInvitation(ctx, p, spaceID, "", base, "invitation.rotate", true)
	}
	return inv, err
}

func (s *Server) issueInvitation(ctx context.Context, p Principal, spaceID, expiresAt, base, action string, rotate bool) (Invitation, error) {
	expires, err := parseExpiry(expiresAt)
	if err != nil {
		if expiresAt != "" {
			return Invitation{}, badRequest("expires_at must be RFC3339 UTC")
		}
		return Invitation{}, err
	}
	sp, ok, err := s.st.getSpace(ctx, spaceID)
	if err != nil {
		return Invitation{}, err
	}
	if !ok {
		return Invitation{}, notFound("space not found")
	}
	code := newSecret("ic_")
	inv := inviteRecord{
		ID: newID("inv_"), Status: "active", SpaceID: spaceID, CreatedBy: p.UserID,
		CodeHash: hashSecret(code), Code: code, CreatedAt: now(),
	}
	if expires != nil {
		inv.ExpiresAt = *expires
	}
	if err := s.st.putInvite(ctx, inv, p.ActorID(), action, sp.OwnerID, rotate); err != nil {
		return Invitation{}, err
	}
	return s.revealInvite(inv, base), nil
}

func (s *Server) RevokeInvitation(ctx context.Context, p Principal, spaceID, inviteID string) (Invitation, error) {
	if p.Kind != KindAdmin {
		return Invitation{}, forbidden("only the space owner can revoke invitations")
	}
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return Invitation{}, err
	}
	sp, ok, err := s.st.getSpace(ctx, spaceID)
	if err != nil {
		return Invitation{}, err
	}
	if !ok {
		return Invitation{}, notFound("space not found")
	}
	inv, _, err := s.st.revokeInvite(ctx, spaceID, inviteID, p.UserID, sp.OwnerID)
	if err != nil {
		return Invitation{}, err
	}
	api := inv.invitation()
	api.SpaceID = ""
	return api, nil
}

type JoinInput struct {
	DisplayName string
	Runtime     string
	InstanceID  string
	OwnerLabel  string
	Description string
}

type JoinResult struct {
	SpaceID      string `json:"space_id"`
	AgentID      string `json:"agent_id"`
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	APIBase      string `json:"api_base"`
	MCPURL       string `json:"mcp_url"`
	BootstrapURL string `json:"bootstrap_url"`
	Reissued     bool   `json:"reissued"`
}

func (s *Server) Join(ctx context.Context, code string, in JoinInput, base string) (JoinResult, error) {
	name, ok := trimMax(in.DisplayName, 80)
	if !ok {
		return JoinResult{}, badRequest("display_name is required and must be at most 80 characters")
	}
	runtime, ok := trimMax(in.Runtime, 80)
	if !ok {
		return JoinResult{}, badRequest("runtime is required and must be at most 80 characters")
	}
	instance := strings.TrimSpace(in.InstanceID)
	if !validInstanceID(instance) {
		return JoinResult{}, badRequest("instance_id must be 1-128 characters of letters, digits, dot, underscore, colon, or hyphen")
	}
	owner, ok := trimMaxAllowEmpty(in.OwnerLabel, 80)
	if !ok {
		return JoinResult{}, badRequest("owner_label is invalid")
	}
	description, ok := trimMaxAllowEmpty(in.Description, 500)
	if !ok {
		return JoinResult{}, badRequest("description is invalid")
	}
	inv, ok, err := s.st.inviteByHash(ctx, hashSecret(code))
	if err != nil {
		return JoinResult{}, err
	}
	if !ok {
		return JoinResult{}, gone("invitation is invalid or revoked")
	}
	if err := inviteUsable(inv); err != nil {
		return JoinResult{}, err
	}
	invitedBy := inv.CreatedBy
	if invitedBy == "" {
		sp, err := s.loadSpace(ctx, inv.SpaceID)
		if err != nil {
			return JoinResult{}, err
		}
		invitedBy = sp.OwnerID
	}
	token := newSecret("as_live_")
	agentID, reissued, ev, err := s.st.join(ctx, inv.SpaceID, joinProfile{
		DisplayName: name, Runtime: runtime, InstanceID: instance, OwnerLabel: owner, Description: description,
		TokenHash: hashSecret(token), NewAgentID: newID("ag_"), InvitationID: inv.ID, InvitedBy: invitedBy,
	})
	if err != nil {
		return JoinResult{}, err
	}
	s.hub.publish(ev)
	root := strings.TrimRight(base, "/")
	return JoinResult{
		SpaceID: inv.SpaceID, AgentID: agentID, AccessToken: token, TokenType: "Bearer", Reissued: reissued,
		APIBase: root + "/api/v1", MCPURL: root + "/mcp", BootstrapURL: root + "/api/v1/spaces/" + inv.SpaceID + "/bootstrap",
	}, nil
}

func inviteUsable(inv inviteRecord) error {
	if inv.Status != "active" || (inv.ExpiresAt != "" && inv.ExpiresAt < now()) {
		return gone("invitation is invalid or revoked")
	}
	return nil
}

func validInstanceID(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == ':' || r == '-'
		if i == 0 && !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
		if !ok {
			return false
		}
	}
	return true
}

func (s *Server) ListMembers(ctx context.Context, p Principal, spaceID string) ([]Member, error) {
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return nil, err
	}
	return s.st.members(ctx, spaceID, 500)
}

func (s *Server) RevokeMember(ctx context.Context, p Principal, spaceID, agentID string) error {
	if p.Kind != KindAdmin {
		return forbidden("only the space owner can revoke member tokens")
	}
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return err
	}
	ev, changed, err := s.st.revokeMember(ctx, spaceID, agentID, p.ActorID())
	if err != nil {
		return err
	}
	if changed {
		s.hub.publish(ev)
	}
	return nil
}

func (s *Server) lookupInvite(ctx context.Context, code string) (Space, error) {
	inv, ok, err := s.st.inviteByHash(ctx, hashSecret(code))
	if err != nil {
		return Space{}, err
	}
	if !ok {
		return Space{}, gone("invitation is invalid or revoked")
	}
	if err := inviteUsable(inv); err != nil {
		return Space{}, err
	}
	sp, err := s.loadSpace(ctx, inv.SpaceID)
	if err != nil {
		return Space{}, gone("invitation is invalid or revoked")
	}
	sp.OwnerID = ""
	return sp, nil
}
