package as

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /openapi.yaml", s.handleOpenAPI)
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app/", http.StatusFound)
	})
	mux.HandleFunc("GET /app/", s.handleWeb)

	mux.HandleFunc("POST /api/v1/admin/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/admin/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/admin/session", s.handleSession)

	mux.HandleFunc("POST /api/v1/spaces", s.handleCreateSpace)
	mux.HandleFunc("GET /api/v1/spaces", s.handleListSpaces)
	mux.HandleFunc("GET /api/v1/spaces/{space_id}", s.handleGetSpace)
	mux.HandleFunc("PATCH /api/v1/spaces/{space_id}", s.handlePatchSpace)
	mux.HandleFunc("POST /api/v1/spaces/{space_id}/invitations", s.handleCreateInvitation)
	mux.HandleFunc("GET /api/v1/spaces/{space_id}/invitations", s.handleListInvitations)
	mux.HandleFunc("POST /api/v1/spaces/{space_id}/invitations/rotate", s.handleRotateInvitation)
	mux.HandleFunc("DELETE /api/v1/spaces/{space_id}/invitations/{invite_id}", s.handleRevokeInvitation)
	mux.HandleFunc("GET /api/v1/spaces/{space_id}/members", s.handleListMembers)
	mux.HandleFunc("POST /api/v1/spaces/{space_id}/members/{agent_id}/revoke", s.handleRevokeMember)
	mux.HandleFunc("GET /api/v1/spaces/{space_id}/audit", s.handleAudit)

	mux.HandleFunc("POST /api/v1/invitations/{code}/join", s.handleJoin)

	mux.HandleFunc("GET /api/v1/spaces/{space_id}/bootstrap", s.handleBootstrap)
	mux.HandleFunc("GET /api/v1/spaces/{space_id}/documents", s.handleListDocuments)
	mux.HandleFunc("POST /api/v1/spaces/{space_id}/documents", s.handleCreateDocument)
	mux.HandleFunc("GET /api/v1/spaces/{space_id}/documents/{doc_id}", s.handleGetDocument)
	mux.HandleFunc("PATCH /api/v1/spaces/{space_id}/documents/{doc_id}", s.handlePatchDocument)
	mux.HandleFunc("DELETE /api/v1/spaces/{space_id}/documents/{doc_id}", s.handleDeleteDocument)
	mux.HandleFunc("GET /api/v1/spaces/{space_id}/documents/{doc_id}/revisions", s.handleListRevisions)
	mux.HandleFunc("GET /api/v1/spaces/{space_id}/documents/{doc_id}/revisions/{rev}", s.handleGetRevision)
	mux.HandleFunc("POST /api/v1/spaces/{space_id}/assets", s.handleUploadAsset)
	mux.HandleFunc("GET /api/v1/spaces/{space_id}/assets/{asset_id}", s.handleDownloadAsset)
	mux.HandleFunc("GET /api/v1/spaces/{space_id}/rooms", s.handleListRooms)
	mux.HandleFunc("POST /api/v1/spaces/{space_id}/dms", s.handleOpenDM)
	mux.HandleFunc("GET /api/v1/spaces/{space_id}/rooms/{room_id}/messages", s.handleListMessages)
	mux.HandleFunc("POST /api/v1/spaces/{space_id}/rooms/{room_id}/messages", s.handleSendMessage)
	mux.HandleFunc("GET /api/v1/spaces/{space_id}/events", s.handlePullEvents)
	mux.HandleFunc("GET /api/v1/spaces/{space_id}/events/stream", s.handleEventStream)
	mux.HandleFunc("POST /api/v1/spaces/{space_id}/events/ack", s.handleAck)

	mux.HandleFunc("GET /i/{code}/manifest.json", s.handleManifest)
	mux.HandleFunc("GET /i/{code}/guide.md", s.handleGuideMD)
	mux.HandleFunc("GET /i/{code}/guide", s.handleGuide)
	mux.HandleFunc("GET /i/{code}/skills/agent-space/SKILL.md", s.handleSkillMD)
	mux.HandleFunc("GET /i/{code}/skills", s.handleSkills)
	mux.HandleFunc("GET /i/{code}", s.handleInvitePage)

	mux.HandleFunc("POST /mcp", s.handleMCP)
	mux.HandleFunc("GET /mcp", s.handleMCPUnsupported)
	mux.HandleFunc("DELETE /mcp", s.handleMCPUnsupported)
	mux.HandleFunc("/api/", s.handleAPINotFound)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "agent-space"})
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	if len(openAPIYAML) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openAPIYAML)
}

func (s *Server) handleAPINotFound(w http.ResponseWriter, r *http.Request) {
	s.writeErr(w, notFound("unknown api route"))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.loginLim.allow(clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		s.writeErr(w, rateLimited("too many login attempts"))
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body, true); err != nil {
		s.writeErr(w, err)
		return
	}
	raw, p, err := s.login(r.Context(), body.Username, body.Password)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.setSessionCookie(w, raw)
	s.writeJSON(w, http.StatusOK, map[string]string{"user_id": p.UserID, "username": strings.TrimSpace(body.Username)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	p, raw, err := s.principalFromRequest(r)
	if err != nil || p.Kind != KindAdmin {
		s.writeErr(w, unauthorized("authentication required"))
		return
	}
	if raw == "" {
		if c, err := r.Cookie("as_session"); err == nil {
			raw = c.Value
		}
	}
	if raw != "" {
		s.logout(r.Context(), raw)
	}
	http.SetCookie(w, &http.Cookie{Name: "as_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	p, ok := s.admin(w, r)
	if !ok {
		return
	}
	user, _, _ := s.st.userByID(r.Context(), p.UserID)
	username := user.Username
	s.writeJSON(w, http.StatusOK, map[string]string{"user_id": p.UserID, "username": username})
}

func (s *Server) handleCreateSpace(w http.ResponseWriter, r *http.Request) {
	p, ok := s.admin(w, r)
	if !ok {
		return
	}
	var body struct {
		Name                string `json:"name"`
		Goal                string `json:"goal"`
		Description         string `json:"description"`
		RunRef              string `json:"run_ref"`
		InvitationExpiresAt string `json:"invitation_expires_at"`
	}
	if err := decodeJSON(w, r, &body, true); err != nil {
		s.writeErr(w, err)
		return
	}
	result, err := s.CreateSpace(r.Context(), p.UserID, CreateSpaceInput{
		Name: body.Name, Goal: body.Goal, Description: body.Description, RunRef: body.RunRef,
		InvitationExpiresAt: body.InvitationExpiresAt,
	}, s.baseURL(r))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleListSpaces(w http.ResponseWriter, r *http.Request) {
	p, ok := s.admin(w, r)
	if !ok {
		return
	}
	spaces, err := s.ListSpaces(r.Context(), p.UserID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"spaces": spaces})
}

func (s *Server) handleGetSpace(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	detail, err := s.GetSpace(r.Context(), p, r.PathValue("space_id"), s.baseURL(r))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handlePatchSpace(w http.ResponseWriter, r *http.Request) {
	p, ok := s.admin(w, r)
	if !ok {
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Goal        *string `json:"goal"`
		Description *string `json:"description"`
		RunRef      *string `json:"run_ref"`
	}
	if err := decodeJSON(w, r, &body, true); err != nil {
		s.writeErr(w, err)
		return
	}
	sp, err := s.PatchSpace(r.Context(), p, r.PathValue("space_id"), PatchSpaceInput{
		Name: body.Name, Goal: body.Goal, Description: body.Description, RunRef: body.RunRef,
	})
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, sp)
}

func (s *Server) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	p, ok := s.admin(w, r)
	if !ok {
		return
	}
	var body struct {
		ExpiresAt string `json:"expires_at"`
	}
	if err := decodeJSON(w, r, &body, false); err != nil {
		s.writeErr(w, err)
		return
	}
	inv, created, err := s.CreateInvitation(r.Context(), p, r.PathValue("space_id"), body.ExpiresAt, s.baseURL(r))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.writeJSON(w, status, inv)
}

func (s *Server) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	p, ok := s.admin(w, r)
	if !ok {
		return
	}
	list, err := s.ListInvitations(r.Context(), p, r.PathValue("space_id"), s.baseURL(r))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"invitations": list})
}

func (s *Server) handleRotateInvitation(w http.ResponseWriter, r *http.Request) {
	p, ok := s.admin(w, r)
	if !ok {
		return
	}
	var body struct {
		ExpiresAt string `json:"expires_at"`
	}
	if err := decodeJSON(w, r, &body, false); err != nil {
		s.writeErr(w, err)
		return
	}
	inv, err := s.RotateInvitation(r.Context(), p, r.PathValue("space_id"), body.ExpiresAt, s.baseURL(r))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, inv)
}

func (s *Server) handleRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	p, ok := s.admin(w, r)
	if !ok {
		return
	}
	inv, err := s.RevokeInvitation(r.Context(), p, r.PathValue("space_id"), r.PathValue("invite_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, inv)
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	if !s.joinLim.allow(clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		s.writeErr(w, rateLimited("too many join attempts"))
		return
	}
	var body struct {
		DisplayName string `json:"display_name"`
		Runtime     string `json:"runtime"`
		InstanceID  string `json:"instance_id"`
		OwnerLabel  string `json:"owner_label"`
		Description string `json:"description"`
	}
	if err := decodeJSON(w, r, &body, true); err != nil {
		s.writeErr(w, err)
		return
	}
	result, err := s.Join(r.Context(), r.PathValue("code"), JoinInput{
		DisplayName: body.DisplayName, Runtime: body.Runtime, InstanceID: body.InstanceID,
		OwnerLabel: body.OwnerLabel, Description: body.Description,
	}, s.baseURL(r))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	status := http.StatusCreated
	if result.Reissued {
		status = http.StatusOK
	}
	s.writeJSON(w, status, result)
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	members, err := s.ListMembers(r.Context(), p, r.PathValue("space_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

func (s *Server) handleRevokeMember(w http.ResponseWriter, r *http.Request) {
	p, ok := s.admin(w, r)
	if !ok {
		return
	}
	if err := s.RevokeMember(r.Context(), p, r.PathValue("space_id"), r.PathValue("agent_id")); err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.admin(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := s.ListAudit(r.Context(), p, r.PathValue("space_id"), limit)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"audit_logs": entries})
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	boot, err := s.Bootstrap(r.Context(), p, r.PathValue("space_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, boot)
}

func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	docs, next, err := s.ListDocuments(r.Context(), p, r.PathValue("space_id"), r.URL.Query().Get("q"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"documents": docs, "next_cursor": next})
}

func (s *Server) handleCreateDocument(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Title      string `json:"title"`
		ContentMD  string `json:"content_md"`
		Pinned     bool   `json:"pinned"`
		ChangeNote string `json:"change_note"`
	}
	if err := decodeJSON(w, r, &body, true); err != nil {
		s.writeErr(w, err)
		return
	}
	doc, err := s.CreateDocument(r.Context(), p, r.PathValue("space_id"), CreateDocumentInput{
		Title: body.Title, ContentMD: body.ContentMD, Pinned: body.Pinned, ChangeNote: body.ChangeNote,
	})
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, doc)
}

func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	doc, err := s.GetDocument(r.Context(), p, r.PathValue("space_id"), r.PathValue("doc_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", contentDisposition(false, doc.Title+".md"))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, doc.ContentMD)
		return
	}
	s.writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handlePatchDocument(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Title            *string `json:"title"`
		ContentMD        *string `json:"content_md"`
		Pinned           *bool   `json:"pinned"`
		ExpectedRevision *int    `json:"expected_revision"`
		ChangeNote       string  `json:"change_note"`
	}
	if err := decodeJSON(w, r, &body, true); err != nil {
		s.writeErr(w, err)
		return
	}
	rev, err := expectedRevision(r, body.ExpectedRevision)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	doc, err := s.UpdateDocument(r.Context(), p, r.PathValue("space_id"), r.PathValue("doc_id"), PatchDocumentInput{
		Title: body.Title, ContentMD: body.ContentMD, Pinned: body.Pinned, ExpectedRevision: rev, ChangeNote: body.ChangeNote,
	})
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	if err := s.DeleteDocument(r.Context(), p, r.PathValue("space_id"), r.PathValue("doc_id")); err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "archived"})
}

func (s *Server) handleListRevisions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	revs, err := s.ListRevisions(r.Context(), p, r.PathValue("space_id"), r.PathValue("doc_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"revisions": revs})
}

func (s *Server) handleGetRevision(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	rev, err := strconv.Atoi(r.PathValue("rev"))
	if err != nil || rev < 1 {
		s.writeErr(w, badRequest("revision must be a positive integer"))
		return
	}
	item, err := s.GetRevision(r.Context(), p, r.PathValue("space_id"), r.PathValue("doc_id"), rev)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleUploadAsset(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(s.cfg.MaxUploadBytes); err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) || strings.Contains(strings.ToLower(err.Error()), "large") {
			s.writeErr(w, tooLarge("upload exceeds the size limit"))
			return
		}
		s.writeErr(w, badRequest("expected a multipart file field named file"))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		s.writeErr(w, badRequest("expected a multipart file field named file"))
		return
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, s.cfg.MaxUploadBytes+1))
	if err != nil {
		s.writeErr(w, badRequest("could not read upload"))
		return
	}
	asset, err := s.SaveAsset(r.Context(), p, r.PathValue("space_id"), header.Filename, body, s.baseURL(r))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, asset)
}

func (s *Server) handleDownloadAsset(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	row, body, err := s.OpenAsset(r.Context(), p, r.PathValue("space_id"), r.PathValue("asset_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	inline := strings.HasPrefix(row.MimeType, "image/")
	w.Header().Set("Content-Type", row.MimeType)
	w.Header().Set("Content-Disposition", contentDisposition(inline, row.Filename))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) handleListRooms(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	rooms, err := s.ListRooms(r.Context(), p, r.PathValue("space_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"rooms": rooms})
}

func (s *Server) handleOpenDM(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		PeerAgentID string `json:"peer_agent_id"`
	}
	if err := decodeJSON(w, r, &body, true); err != nil {
		s.writeErr(w, err)
		return
	}
	room, err := s.OpenDM(r.Context(), p, r.PathValue("space_id"), body.PeerAgentID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, room)
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	page, err := s.ListMessages(r.Context(), p, r.PathValue("space_id"), r.PathValue("room_id"), r.URL.Query().Get("q"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		ContentMD    string   `json:"content_md"`
		Mentions     []string `json:"mentions"`
		DocumentRefs []struct {
			DocumentID string `json:"document_id"`
			Revision   *int   `json:"revision"`
		} `json:"document_refs"`
		AssetIDs         []string `json:"asset_ids"`
		IdempotencyKey   string   `json:"idempotency_key"`
		ReplyToMessageID string   `json:"reply_to_message_id"`
	}
	if err := decodeJSON(w, r, &body, true); err != nil {
		s.writeErr(w, err)
		return
	}
	refs := make([]DocRef, 0, len(body.DocumentRefs))
	for _, ref := range body.DocumentRefs {
		item := DocRef{DocumentID: ref.DocumentID}
		if ref.Revision != nil {
			item.Revision = *ref.Revision
		}
		refs = append(refs, item)
	}
	msg, err := s.SendMessage(r.Context(), p, r.PathValue("space_id"), r.PathValue("room_id"), SendMessageInput{
		ContentMD: body.ContentMD, Mentions: body.Mentions, DocumentRefs: refs,
		AssetIDs: body.AssetIDs, IdempotencyKey: body.IdempotencyKey, ReplyToMessageID: body.ReplyToMessageID,
	})
	if err != nil {
		s.writeErr(w, err)
		return
	}
	status := http.StatusCreated
	if msg.Replayed {
		status = http.StatusOK
	}
	s.writeJSON(w, status, msg)
}

func (s *Server) handlePullEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	after, err := parseAfter(r.URL.Query().Get("after_seq"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := s.PullEvents(r.Context(), p, r.PathValue("space_id"), after, limit)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		LastAckSeq *int64 `json:"last_ack_seq"`
	}
	if err := decodeJSON(w, r, &body, true); err != nil {
		s.writeErr(w, err)
		return
	}
	if body.LastAckSeq == nil {
		s.writeErr(w, badRequest("last_ack_seq is required"))
		return
	}
	seq, ts, err := s.AckEvents(r.Context(), p, r.PathValue("space_id"), *body.LastAckSeq)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"last_ack_seq": seq, "updated_at": ts})
}

func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	spaceID := r.PathValue("space_id")
	if err := s.authorizeSpace(r.Context(), p, spaceID); err != nil {
		s.writeErr(w, err)
		return
	}
	after, err := parseAfter(r.URL.Query().Get("after_seq"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeErr(w, internalf("streaming is not available"))
		return
	}
	rooms, err := s.visibleRoomSet(r.Context(), p, spaceID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	writePull := func(from int64) (int64, error) {
		for {
			res, err := s.PullEvents(r.Context(), p, spaceID, from, 200)
			if err != nil {
				return from, err
			}
			for _, ev := range res.Events {
				payload, _ := json.Marshal(ev)
				fmt.Fprintf(w, "id: %d\nevent: space.event\ndata: %s\n\n", ev.Seq, payload)
			}
			flusher.Flush()
			from = res.NextSeq
			if !res.HasMore {
				return from, nil
			}
		}
	}
	after, err = writePull(after)
	if err != nil {
		return
	}
	subID, ch := s.hub.add(spaceID, p.Kind == KindAdmin, rooms)
	defer s.hub.remove(subID)
	// Catch anything published while the first pull was running.
	after, err = writePull(after)
	if err != nil {
		return
	}
	keepalive := time.NewTicker(15 * time.Second)
	poll := time.NewTicker(s.cfg.SSEPollInterval)
	defer keepalive.Stop()
	defer poll.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case <-poll.C:
			after, err = writePull(after)
			if err != nil {
				return
			}
		case ev := <-ch:
			if ev.Seq <= after {
				continue
			}
			after, err = writePull(after)
			if err != nil {
				return
			}
		}
	}
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	p, ok := s.principal(w, r)
	if !ok {
		return Principal{}, false
	}
	if p.Kind != KindAdmin {
		s.writeErr(w, forbidden("administrator session required"))
		return Principal{}, false
	}
	return p, true
}

func (s *Server) principal(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	p, _, err := s.principalFromRequest(r)
	if err != nil {
		s.writeErr(w, err)
		return Principal{}, false
	}
	return p, true
}

func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.PublicBaseURL != "" {
		return strings.TrimRight(s.cfg.PublicBaseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) setSessionCookie(w http.ResponseWriter, raw string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "as_session",
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.CookieSecure,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

func (s *Server) writeErr(w http.ResponseWriter, err error) {
	var ae *appError
	if errors.As(err, &ae) {
		payload := map[string]any{"code": ae.Code, "message": ae.Message}
		if ae.Details != nil {
			payload["details"] = ae.Details
		}
		s.writeJSON(w, ae.Status, map[string]any{"error": payload})
		return
	}
	s.log.Error("request failed", "err", err.Error())
	s.writeJSON(w, http.StatusInternalServerError, map[string]any{
		"error": map[string]string{"code": CodeInternal, "message": "internal error"},
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, required bool) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == io.EOF {
		if required {
			return badRequest("json body is required")
		}
		return nil
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "too large") {
			return tooLarge("request body is too large")
		}
		if strings.Contains(err.Error(), "unknown field") {
			return badRequest(err.Error())
		}
		return badRequest("invalid json body")
	}
	return nil
}

func expectedRevision(r *http.Request, body *int) (int, error) {
	header := strings.TrimSpace(r.Header.Get("If-Match"))
	headerRev := 0
	hasHeader := false
	if header != "" {
		header = strings.Trim(header, `"`)
		n, err := strconv.Atoi(header)
		if err != nil || n < 1 {
			return 0, badRequest("If-Match must be a positive revision")
		}
		headerRev = n
		hasHeader = true
	}
	hasBody := body != nil
	if hasBody && *body < 1 {
		return 0, badRequest("expected_revision must be >= 1")
	}
	if hasHeader && hasBody && headerRev != *body {
		return 0, badRequest("If-Match does not match expected_revision")
	}
	if hasBody {
		return *body, nil
	}
	if hasHeader {
		return headerRev, nil
	}
	return 0, badRequest("expected_revision or If-Match is required")
}

func contentDisposition(inline bool, filename string) string {
	kind := "attachment"
	if inline {
		kind = "inline"
	}
	ascii := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, filename)
	ascii = strings.Trim(ascii, " .")
	if ascii == "" {
		ascii = "file"
	}
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, kind, ascii, url.PathEscape(filename))
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if s.webReady() {
		http.Redirect(w, r, "/app/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>AgentSpace</title><body><p>AgentSpace API 已启动。构建 <code>web/</code> 后可使用控制台。</p><p><a href="/healthz">健康检查</a></p></body></html>`)
}

func (s *Server) webReady() bool {
	if s.cfg.WebDir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(s.cfg.WebDir, "index.html"))
	return err == nil && !info.IsDir()
}

func (s *Server) handleWeb(w http.ResponseWriter, r *http.Request) {
	if !s.webReady() {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/app")
	rel = filepath.Clean("/" + rel)
	root, err := filepath.Abs(s.cfg.WebDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target := filepath.Join(root, rel)
	abs, err := filepath.Abs(target)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	if rel == "/" {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
		return
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		ext := strings.ToLower(filepath.Ext(abs))
		switch ext {
		case ".js", ".css", ".map", ".svg", ".png", ".woff2":
			http.NotFound(w, r)
			return
		default:
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(root, "index.html"))
			return
		}
	}
	if strings.HasPrefix(strings.TrimPrefix(rel, "/"), "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeFile(w, r, abs)
}
