package hc

import (
	"context"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func (s *Server) ensureAdmin(ctx context.Context) error {
	ready, err := s.st.hasAdmin(ctx)
	if err != nil || ready {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(s.cfg.AdminPassword), s.cfg.BcryptCost)
	if err != nil {
		return err
	}
	_, err = s.st.ensureUser(ctx, userRecord{
		ID: newID("usr_"), Username: s.cfg.AdminUsername, PasswordHash: string(hash), CreatedAt: now(),
	})
	return err
}

func (s *Server) login(ctx context.Context, username, password string) (string, Principal, error) {
	username = strings.TrimSpace(username)
	user, ok, err := s.st.userByName(ctx, username)
	if err != nil {
		return "", Principal{}, err
	}
	if !ok {
		_ = s.audit(ctx, "", "anonymous", "admin.login", "user", "unknown", "denied")
		return "", Principal{}, unauthorized("invalid username or password")
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		_ = s.audit(ctx, "", "anonymous", "admin.login", "user", user.ID, "denied")
		return "", Principal{}, unauthorized("invalid username or password")
	}
	raw := newSecret("hc_sess_")
	expires := time.Now().UTC().Add(s.cfg.SessionTTL)
	rec := sessionRecord{ID: newID("ses_"), UserID: user.ID, ExpiresAt: expires.Format("2006-01-02T15:04:05.000Z")}
	if err := s.st.putSession(ctx, hashSecret(raw), rec, s.cfg.SessionTTL); err != nil {
		return "", Principal{}, err
	}
	_ = s.audit(ctx, "", user.ID, "admin.login", "user", user.ID, "ok")
	return raw, Principal{Kind: KindAdmin, UserID: user.ID, Status: "active"}, nil
}

func (s *Server) logout(ctx context.Context, raw string) {
	_ = s.st.deleteSession(ctx, hashSecret(raw))
}

func (s *Server) principalFromRequest(r *http.Request) (Principal, string, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		const prefix = "Bearer "
		if !strings.HasPrefix(h, prefix) || strings.TrimSpace(h[len(prefix):]) == "" {
			return Principal{}, "", unauthorized("authorization must be a bearer token")
		}
		raw := strings.TrimSpace(h[len(prefix):])
		switch {
		case strings.HasPrefix(raw, "hc_live_"):
			p, err := s.principalFromAgentToken(r.Context(), raw)
			return p, "", err
		case strings.HasPrefix(raw, "hc_sess_"):
			p, err := s.principalFromSession(r.Context(), raw)
			return p, raw, err
		default:
			return Principal{}, "", unauthorized("unknown credentials")
		}
	}
	c, err := r.Cookie("hc_session")
	if err != nil || c.Value == "" {
		return Principal{}, "", unauthorized("authentication required")
	}
	p, err := s.principalFromSession(r.Context(), c.Value)
	return p, c.Value, err
}

func (s *Server) principalFromSession(ctx context.Context, raw string) (Principal, error) {
	rec, ok, err := s.st.getSession(ctx, hashSecret(raw))
	if err != nil {
		return Principal{}, err
	}
	if !ok {
		return Principal{}, unauthorized("session expired or revoked")
	}
	return Principal{Kind: KindAdmin, UserID: rec.UserID, Status: "active"}, nil
}

func (s *Server) principalFromAgentToken(ctx context.Context, raw string) (Principal, error) {
	spaceID, agentID, status, ok, err := s.st.lookupToken(ctx, hashSecret(raw))
	if err != nil {
		return Principal{}, err
	}
	if !ok || status != "active" {
		return Principal{}, unauthorized("agent token is invalid or revoked")
	}
	_ = s.st.touchSeen(ctx, agentID, now())
	return Principal{Kind: KindAgent, AgentID: agentID, SpaceID: spaceID, Status: status}, nil
}

func (s *Server) authorizeSpace(ctx context.Context, p Principal, spaceID string) error {
	if p.Kind == KindAgent {
		if p.SpaceID != spaceID || p.Status != "active" {
			return forbidden("agent token is not valid for this space")
		}
		return nil
	}
	if p.Kind != KindAdmin {
		return unauthorized("authentication required")
	}
	sp, ok, err := s.st.getSpace(ctx, spaceID)
	if err != nil {
		return err
	}
	if !ok || sp.OwnerID != p.UserID {
		return notFound("space not found")
	}
	return nil
}

func (s *Server) audit(ctx context.Context, spaceID, actor, action, objectType, objectID, result string) error {
	return s.st.addAudit(ctx, newAudit(spaceID, actor, action, objectType, objectID, result, now()))
}
