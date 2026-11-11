package as

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func testRedisAddr() string {
	if v := strings.TrimSpace(os.Getenv("AGENT_SPACE_TEST_REDIS")); v != "" {
		return v
	}
	return "127.0.0.1:6380"
}

func testPrefix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "as:t:" + hex.EncodeToString(b[:])
}

type inviteEnv struct {
	t      *testing.T
	app    *Server
	srv    *httptest.Server
	admin  *http.Client
	base   string
	prefix string
}

func startInviteEnv(t *testing.T) *inviteEnv {
	t.Helper()
	prefix := testPrefix()
	cfg := Config{
		RedisAddr:          testRedisAddr(),
		RedisPassword:      os.Getenv("AGENT_SPACE_TEST_REDIS_PASSWORD"),
		KeyPrefix:          prefix,
		AssetDir:           filepath.Join(t.TempDir(), "assets"),
		WebDir:             filepath.Join(t.TempDir(), "no-web"),
		AdminUsername:      "admin",
		AdminPassword:      "test-admin-password",
		BcryptCost:         bcrypt.MinCost,
		JoinRatePerMinute:  -1,
		LoginRatePerMinute: -1,
		WriteRatePerMinute: -1,
	}
	app, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler())
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	admin := &http.Client{Jar: jar}
	e := &inviteEnv{t: t, app: app, srv: srv, admin: admin, base: srv.URL, prefix: prefix}
	e.json(admin, http.MethodPost, "/api/v1/admin/login", map[string]string{
		"username": "admin", "password": "test-admin-password",
	}, http.StatusOK)
	t.Cleanup(func() {
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = app.DeletePrefix(ctx)
		_ = app.Close()
	})
	return e
}

func (e *inviteEnv) json(client *http.Client, method, path string, body any, want int) map[string]any {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, e.base+path, reader)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("X-Agent-Space", "1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	if res.StatusCode != want {
		e.t.Fatalf("%s %s: status %d want %d", method, path, res.StatusCode, want)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("json: %v", err)
	}
	return out
}

func mapString(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func mapObj(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("missing object %s", key)
	}
	return v
}

func (e *inviteEnv) join(inviteURL, name, instance string) {
	e.t.Helper()
	code := inviteURL[strings.LastIndex(inviteURL, "/")+1:]
	reqBody, err := json.Marshal(map[string]string{
		"display_name": name, "runtime": "HTTP", "instance_id": instance,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, e.base+"/api/v1/invitations/"+code+"/join", strings.NewReader(string(reqBody)))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		e.t.Fatalf("join status %d", res.StatusCode)
	}
}

func TestUserScopedInvites(t *testing.T) {
	e := startInviteEnv(t)
	ctx := context.Background()

	created := e.json(e.admin, http.MethodPost, "/api/v1/spaces", map[string]any{"name": "邀请规则"}, http.StatusCreated)
	space := mapObj(t, created, "space")
	spaceID := mapString(space, "id")
	first := mapObj(t, created, "invitation")
	url1 := mapString(first, "invite_url")
	code1 := mapString(first, "invite_code")
	id1 := mapString(first, "id")
	if url1 == "" || code1 == "" || first["expires_at"] != nil && mapString(first, "expires_at") != "" {
		t.Fatal("created invitation missing url or had an expiry")
	}

	detail := e.json(e.admin, http.MethodGet, "/api/v1/spaces/"+spaceID, nil, http.StatusOK)
	active := mapObj(t, detail, "active_invitation")
	if mapString(active, "invite_url") != url1 || mapString(active, "id") != id1 {
		t.Fatal("GET space did not return the same invite url")
	}

	same := e.json(e.admin, http.MethodPost, "/api/v1/spaces/"+spaceID+"/invitations", map[string]any{}, http.StatusOK)
	if mapString(same, "invite_url") != url1 || mapString(same, "id") != id1 {
		t.Fatal("creating again should return the existing invite, not 409")
	}

	e.join(url1, "Agent One", "inst-one")
	e.join(url1, "Agent Two", "inst-two")
	e.join(url1, "Agent One", "inst-one")

	session := e.json(e.admin, http.MethodGet, "/api/v1/admin/session", nil, http.StatusOK)
	ownerID := mapString(session, "user_id")
	if ownerID == "" {
		t.Fatal("missing admin user id")
	}
	members := e.json(e.admin, http.MethodGet, "/api/v1/spaces/"+spaceID+"/members", nil, http.StatusOK)
	listed, _ := members["members"].([]any)
	if len(listed) != 2 {
		t.Fatalf("want 2 members, got %d", len(listed))
	}
	for _, item := range listed {
		m := item.(map[string]any)
		if mapString(m, "invited_by") != ownerID || mapString(m, "invitation_id") != id1 {
			t.Fatal("join did not record the inviter")
		}
	}

	user2 := userRecord{ID: newID("usr_"), Username: "second", PasswordHash: "x", CreatedAt: now()}
	raw, err := encodeJSON(user2)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.app.st.rdb.Set(ctx, e.app.st.key("user", "id", user2.ID), raw, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := e.app.st.rdb.Set(ctx, e.app.st.key("user", "name", user2.Username), user2.ID, 0).Err(); err != nil {
		t.Fatal(err)
	}
	p2 := Principal{Kind: KindAdmin, UserID: user2.ID, Status: "active"}
	second, err := e.app.issueInvitation(ctx, p2, spaceID, "", e.base, "invitation.create", false)
	if err != nil {
		t.Fatal(err)
	}
	if second.URL == "" || second.URL == url1 || second.ID == id1 {
		t.Fatal("second user should receive a different invite")
	}

	rotated := e.json(e.admin, http.MethodPost, "/api/v1/spaces/"+spaceID+"/invitations/rotate", map[string]any{}, http.StatusOK)
	url1b := mapString(rotated, "invite_url")
	if url1b == "" || url1b == url1 {
		t.Fatal("rotate should issue a new url for the current user")
	}
	res, err := http.Get(url1 + "/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusGone {
		t.Fatalf("old owner invite status %d", res.StatusCode)
	}
	res, err = http.Get(second.URL + "/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("other user's invite should still work, got %d", res.StatusCode)
	}
	e.join(second.URL, "Agent Three", "inst-three")
	members = e.json(e.admin, http.MethodGet, "/api/v1/spaces/"+spaceID+"/members", nil, http.StatusOK)
	foundThird := false
	for _, item := range members["members"].([]any) {
		m := item.(map[string]any)
		if mapString(m, "display_name") != "Agent Three" {
			if mapString(m, "invited_by") != ownerID {
				t.Fatal("existing members lost their inviter")
			}
			continue
		}
		foundThird = true
		if mapString(m, "invited_by") != user2.ID || mapString(m, "invited_by_username") != "second" {
			t.Fatal("new member was not attributed to the second user")
		}
	}
	if !foundThird {
		t.Fatal("missing member who joined via the second user's link")
	}

	still := e.json(e.admin, http.MethodGet, "/api/v1/spaces/"+spaceID+"/members", nil, http.StatusOK)
	if len(still["members"].([]any)) != 3 {
		t.Fatal("rotating an invite must not drop existing members")
	}
}

func TestLegacyInviteMigratesOnOpen(t *testing.T) {
	e := startInviteEnv(t)
	ctx := context.Background()
	created := e.json(e.admin, http.MethodPost, "/api/v1/spaces", map[string]any{"name": "旧邀请"}, http.StatusCreated)
	spaceID := mapString(mapObj(t, created, "space"), "id")
	current := mapObj(t, created, "invitation")
	oldURL := mapString(current, "invite_url")
	oldID := mapString(current, "id")
	e.join(oldURL, "Legacy Agent", "legacy-inst")

	session := e.json(e.admin, http.MethodGet, "/api/v1/admin/session", nil, http.StatusOK)
	ownerID := mapString(session, "user_id")

	inv, ok, err := e.app.st.getInvite(ctx, oldID)
	if err != nil || !ok {
		t.Fatal("missing current invite")
	}
	legacyCode := inv.Code
	legacyHash := inv.CodeHash
	legacyJSON := `{"id":"` + inv.ID + `","status":"active","revoked_at":"","space_id":"` + spaceID + `","code_hash":"` + legacyHash + `","expires_at":"","created_at":"` + inv.CreatedAt + `"}`
	if err := e.app.st.rdb.Set(ctx, e.app.st.key("invite", inv.ID), legacyJSON, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := e.app.st.rdb.Del(ctx, e.app.st.key("space", spaceID, "active_invite", ownerID)).Err(); err != nil {
		t.Fatal(err)
	}
	if err := e.app.st.rdb.Set(ctx, e.app.st.key("space", spaceID, "active_invite"), inv.ID, 0).Err(); err != nil {
		t.Fatal(err)
	}

	detail := e.json(e.admin, http.MethodGet, "/api/v1/spaces/"+spaceID, nil, http.StatusOK)
	fresh := mapObj(t, detail, "active_invitation")
	if mapString(fresh, "id") == oldID || mapString(fresh, "invite_url") == "" || mapString(fresh, "invite_url") == oldURL {
		t.Fatal("legacy invite without plaintext should be replaced")
	}
	if mapString(fresh, "invite_code") == legacyCode {
		t.Fatal("legacy code should not be reused")
	}

	res, err := http.Get(oldURL + "/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusGone {
		t.Fatalf("legacy invite status %d", res.StatusCode)
	}

	members := e.json(e.admin, http.MethodGet, "/api/v1/spaces/"+spaceID+"/members", nil, http.StatusOK)
	listed, _ := members["members"].([]any)
	if len(listed) != 1 {
		t.Fatal("migrating a legacy invite must not drop members")
	}
	member := listed[0].(map[string]any)
	if mapString(member, "display_name") != "Legacy Agent" || mapString(member, "invited_by") != ownerID {
		t.Fatal("existing member was not preserved")
	}
	if mapString(member, "invitation_id") != oldID {
		t.Fatal("historical invitation_id should stay on the member")
	}
}
