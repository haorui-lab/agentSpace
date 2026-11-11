package acceptance

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haorui/agentSpace/internal/hc"
	"golang.org/x/crypto/bcrypt"
)

type env struct {
	t      *testing.T
	app    *hc.Server
	srv    *httptest.Server
	dir    string
	logs   *bytes.Buffer
	admin  *api
	prefix string
	keep   bool
}

type api struct {
	base  string
	http  *http.Client
	token string
}

func startAt(t *testing.T, dir string, logs *bytes.Buffer, joinLimit int) *env {
	t.Helper()
	return openEnv(t, dir, logs, joinLimit, "")
}

func openEnv(t *testing.T, dir string, logs *bytes.Buffer, joinLimit int, prefix string) *env {
	t.Helper()
	if logs == nil {
		logs = &bytes.Buffer{}
	}
	if prefix == "" {
		prefix = testPrefix()
	}
	cfg := hc.Config{
		RedisAddr:          testRedisAddr(),
		RedisPassword:      os.Getenv("HELLO_CENTER_TEST_REDIS_PASSWORD"),
		KeyPrefix:          prefix,
		AssetDir:           filepath.Join(dir, "assets"),
		WebDir:             filepath.Join(dir, "no-web"),
		AdminUsername:      "admin",
		AdminPassword:      "test-admin-password",
		BcryptCost:         bcrypt.MinCost,
		JoinRatePerMinute:  joinLimit,
		LoginRatePerMinute: -1,
		WriteRatePerMinute: -1,
		SSEPollInterval:    30 * time.Millisecond,
		Logger:             slog.New(slog.NewTextHandler(logs, nil)),
	}
	app, err := hc.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler())
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	admin := &api{base: srv.URL, http: &http.Client{Jar: jar}}
	admin.call(t, http.MethodPost, "/api/v1/admin/login", map[string]string{
		"username": "admin", "password": "test-admin-password",
	}, http.StatusOK)
	e := &env{t: t, app: app, srv: srv, dir: dir, logs: logs, admin: admin, prefix: prefix}
	t.Cleanup(func() {
		srv.Close()
		if !e.keep {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = app.DeletePrefix(ctx)
		}
		_ = app.Close()
	})
	return e
}

func (e *env) restart(t *testing.T) *env {
	t.Helper()
	e.keep = true
	e.srv.Close()
	if err := e.app.Close(); err != nil {
		t.Fatal(err)
	}
	return openEnv(t, e.dir, nil, -1, e.prefix)
}

func testRedisAddr() string {
	if v := strings.TrimSpace(os.Getenv("HELLO_CENTER_TEST_REDIS")); v != "" {
		return v
	}
	return "127.0.0.1:6380"
}

func testPrefix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "hc:t:" + hex.EncodeToString(b[:])
}

func start(t *testing.T) *env {
	t.Helper()
	return startAt(t, t.TempDir(), nil, -1)
}

func (a *api) call(t *testing.T, method, path string, body any, want int) map[string]any {
	t.Helper()
	status, raw, _ := a.do(t, method, path, body, "")
	if status != want {
		t.Fatalf("%s %s: status %d want %d body %s", method, path, status, want, raw)
	}
	return decode(t, raw)
}

func (a *api) do(t *testing.T, method, path string, body any, contentType string) (int, []byte, http.Header) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		switch b := body.(type) {
		case []byte:
			reader = bytes.NewReader(b)
		default:
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(raw)
			if contentType == "" {
				contentType = "application/json"
			}
		}
	}
	req, err := http.NewRequest(method, a.base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Hello-Center", "1")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	res, err := a.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, raw, res.Header
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("json: %v body %s", err, raw)
	}
	return out
}

func obj(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("missing object %s in %#v", key, m)
	}
	return v
}

func arr(t *testing.T, m map[string]any, key string) []any {
	t.Helper()
	v, ok := m[key].([]any)
	if !ok {
		t.Fatalf("missing array %s in %#v", key, m)
	}
	return v
}

func str(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func num(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func join(t *testing.T, base, inviteURL, name, runtime, instance string) *api {
	t.Helper()
	manifestURL := strings.TrimRight(inviteURL, "/") + "/manifest.json"
	res, err := http.Get(manifestURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("manifest %d %s", res.StatusCode, body)
	}
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	joinAPI := str(obj(t, manifest, "onboarding"), "join_api")
	reqBody, _ := json.Marshal(map[string]string{
		"display_name": name, "runtime": runtime, "instance_id": instance, "owner_label": "测试",
	})
	req, _ := http.NewRequest(http.MethodPost, joinAPI, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		t.Fatalf("join %d %s", res.StatusCode, raw)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("join cache header %s", res.Header.Get("Cache-Control"))
	}
	payload := decode(t, raw)
	token := str(payload, "access_token")
	if token == "" || str(payload, "agent_id") == "" {
		t.Fatalf("join payload %#v", payload)
	}
	return &api{base: base, http: http.DefaultClient, token: token}
}

func upload(t *testing.T, client *api, spaceID, filename string, content []byte, want int) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	status, raw, _ := client.do(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/assets", buf.Bytes(), w.FormDataContentType())
	if status != want {
		t.Fatalf("upload %d want %d body %s", status, want, raw)
	}
	if want >= 400 {
		return decode(t, raw)
	}
	return decode(t, raw)
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 15, G: 110, B: 86, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mcp(t *testing.T, base, token, method string, params any) map[string]any {
	t.Helper()
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		payload["params"] = params
	}
	rawBody, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", bytes.NewReader(rawBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("mcp %s status %d body %s", method, res.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err, string(body))
	}
	if out["error"] != nil {
		t.Fatalf("mcp %s error %#v", method, out["error"])
	}
	result, _ := out["result"].(map[string]any)
	return result
}

func TestAcceptance(t *testing.T) {
	e := start(t)
	var (
		spaceID    string
		inviteURL  string
		inviteCode string
		inviteID   string
		roomID     string
		docID      string
		assetID    string
		agentA     *api
		agentB     *api
		agentC     *api
		dmRoom     string
		cursor     int64
	)

	t.Run("01_create_space_without_goal", func(t *testing.T) {
		created := e.admin.call(t, http.MethodPost, "/api/v1/spaces", map[string]any{"name": "仅有名称"}, http.StatusCreated)
		space := obj(t, created, "space")
		if str(space, "name") != "仅有名称" || str(space, "goal") != "" {
			t.Fatalf("space %#v", space)
		}
		spaceID = str(space, "id")
		roomID = str(created, "general_room_id")
		if roomID == "" {
			t.Fatal("missing general room")
		}
		inv := obj(t, created, "invitation")
		inviteCode = str(inv, "invite_code")
		inviteURL = str(inv, "invite_url")
		inviteID = str(inv, "id")
		if inviteCode == "" || !strings.Contains(inviteURL, "/i/") {
			t.Fatalf("invitation %#v", inv)
		}
		if inv["expires_at"] != nil && str(inv, "expires_at") != "" {
			t.Fatal("new invitation should not expire by default")
		}
		detail := e.admin.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID, nil, http.StatusOK)
		if str(obj(t, detail, "space"), "goal") != "" || str(detail, "general_room_id") != roomID {
			t.Fatalf("detail %#v", detail)
		}
		active := obj(t, detail, "active_invitation")
		if str(active, "invite_url") != inviteURL || str(active, "invite_code") != inviteCode || str(active, "id") != inviteID {
			t.Fatal("space detail did not return the same invite url")
		}
		if active["expires_at"] != nil && str(active, "expires_at") != "" {
			t.Fatal("active invitation should not expire by default")
		}
		again := e.admin.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID, nil, http.StatusOK)
		if str(obj(t, again, "active_invitation"), "invite_url") != inviteURL {
			t.Fatal("invite url changed after refresh")
		}
		rooms := e.admin.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/rooms", nil, http.StatusOK)
		found := false
		for _, item := range arr(t, rooms, "rooms") {
			room := item.(map[string]any)
			if str(room, "type") == "general" && str(room, "id") == roomID {
				found = true
			}
		}
		if !found {
			t.Fatalf("rooms %#v", rooms)
		}
	})

	t.Run("02_manifest_is_self_describing", func(t *testing.T) {
		res, err := http.Get(inviteURL + "/manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status %d", res.StatusCode)
		}
		if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("headers %#v", res.Header)
		}
		var manifest map[string]any
		if err := json.Unmarshal(body, &manifest); err != nil {
			t.Fatal(err)
		}
		space := obj(t, manifest, "space")
		if str(space, "name") != "仅有名称" || str(space, "id") != spaceID {
			t.Fatalf("manifest space %#v", space)
		}
		onboarding := obj(t, manifest, "onboarding")
		if !strings.Contains(str(onboarding, "join_api"), "/api/v1/invitations/") {
			t.Fatalf("join api %#v", onboarding)
		}
		caps := obj(t, manifest, "capabilities")
		for _, key := range []string{"mcp_url", "http_api_base", "skills_catalog_url", "usage_guide_url"} {
			if str(caps, key) == "" {
				t.Fatalf("missing %s", key)
			}
		}
		if _, ok := manifest["documents"]; ok {
			t.Fatal("manifest leaked documents")
		}
		page, err := http.Get(inviteURL)
		if err != nil {
			t.Fatal(err)
		}
		html, _ := io.ReadAll(page.Body)
		page.Body.Close()
		if !strings.Contains(string(html), "谁拿到这条链接，都能让自己的 Agent 加入。") || !strings.Contains(string(html), "Claude Code") {
			t.Fatalf("invite page missing guidance: %s", html)
		}
		skillRes, err := http.Get(str(caps, "skills_catalog_url"))
		if err != nil {
			t.Fatal(err)
		}
		skillBody, _ := io.ReadAll(skillRes.Body)
		skillRes.Body.Close()
		if !strings.Contains(string(skillBody), "hello-center") {
			t.Fatalf("skills %s", skillBody)
		}
	})

	t.Run("03_two_agents_join", func(t *testing.T) {
		agentA = join(t, e.srv.URL, inviteURL, "Coding Agent", "Codex CLI", "laptop-coding-01")
		agentB = join(t, e.srv.URL, inviteURL, "Product Agent", "Claude Code", "laptop-product-01")
		again := join(t, e.srv.URL, inviteURL, "Coding Agent", "Codex CLI", "laptop-coding-01")
		old := agentA.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/bootstrap", nil, http.StatusUnauthorized)
		if str(obj(t, old, "error"), "code") != "UNAUTHORIZED" {
			t.Fatalf("old token %#v", old)
		}
		agentA = again
		boot := agentA.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/bootstrap", nil, http.StatusOK)
		if str(obj(t, boot, "general_room"), "id") != roomID {
			t.Fatalf("bootstrap %#v", boot)
		}
		members := arr(t, boot, "members")
		if len(members) != 2 {
			t.Fatalf("members %#v", members)
		}
		session := e.admin.call(t, http.MethodGet, "/api/v1/admin/session", nil, http.StatusOK)
		ownerID := str(session, "user_id")
		ownerName := str(session, "username")
		if ownerID == "" {
			t.Fatal("missing session user")
		}
		listed := e.admin.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/members", nil, http.StatusOK)
		for _, item := range arr(t, listed, "members") {
			member := item.(map[string]any)
			if str(member, "invited_by") != ownerID {
				t.Fatal("member invited_by was not the inviter")
			}
			if ownerName != "" && str(member, "invited_by_username") != ownerName {
				t.Fatal("member invited_by_username mismatch")
			}
			if str(member, "invitation_id") != inviteID {
				t.Fatal("member invitation_id was not preserved")
			}
		}
	})

	t.Run("04_document_and_asset_shared", func(t *testing.T) {
		doc := agentA.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/documents", map[string]any{
			"title": "排障记录", "content_md": "# 启动失败\n\n端口没有监听。", "pinned": true,
		}, http.StatusCreated)
		docID = str(doc, "id")
		if num(doc, "revision") != 1 {
			t.Fatal(doc)
		}
		asset := upload(t, agentA, spaceID, "screen.png", tinyPNG(t), http.StatusCreated)
		assetID = str(asset, "id")
		list := agentB.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents?q="+urlQuery("启动失败"), nil, http.StatusOK)
		found := false
		for _, item := range arr(t, list, "documents") {
			if str(item.(map[string]any), "id") == docID {
				found = true
			}
		}
		if !found {
			t.Fatalf("list %#v", list)
		}
		got := agentB.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+docID, nil, http.StatusOK)
		if !strings.Contains(str(got, "content_md"), "端口没有监听") {
			t.Fatal(got)
		}
		status, body, header := agentB.do(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/assets/"+assetID, nil, "")
		if status != http.StatusOK || !bytes.Equal(body, tinyPNG(t)) {
			t.Fatalf("download %d %s", status, body)
		}
		if !strings.Contains(header.Get("Content-Type"), "image/png") {
			t.Fatal(header.Get("Content-Type"))
		}
		anon := &api{base: e.srv.URL, http: http.DefaultClient}
		anon.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/assets/"+assetID, nil, http.StatusUnauthorized)
	})

	t.Run("05_revision_conflict", func(t *testing.T) {
		updated := agentA.call(t, http.MethodPatch, "/api/v1/spaces/"+spaceID+"/documents/"+docID, map[string]any{
			"content_md": "# 启动失败\n\nA 已补充。", "expected_revision": 1,
		}, http.StatusOK)
		if num(updated, "revision") != 2 {
			t.Fatal(updated)
		}
		conflict := agentB.call(t, http.MethodPatch, "/api/v1/spaces/"+spaceID+"/documents/"+docID, map[string]any{
			"content_md": "B 基于旧版本", "expected_revision": 1,
		}, http.StatusConflict)
		errObj := obj(t, conflict, "error")
		if str(errObj, "code") != "CONFLICT" || num(obj(t, errObj, "details"), "current_revision") != 2 {
			t.Fatalf("conflict %#v", conflict)
		}
		history := agentB.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+docID+"/revisions", nil, http.StatusOK)
		revs := arr(t, history, "revisions")
		if len(revs) < 2 {
			t.Fatal(history)
		}
		first := agentB.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+docID+"/revisions/1", nil, http.StatusOK)
		if !strings.Contains(str(first, "content_md"), "端口没有监听") {
			t.Fatal(first)
		}
		agentB.call(t, http.MethodPatch, "/api/v1/spaces/"+spaceID+"/documents/"+docID, map[string]any{
			"content_md": "# 启动失败\n\nA 与 B 合并。", "expected_revision": 2,
		}, http.StatusOK)
		req, _ := http.NewRequest(http.MethodPatch, e.srv.URL+"/api/v1/spaces/"+spaceID+"/documents/"+docID, strings.NewReader(`{"title":"用 If-Match 改标题"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+agentA.token)
		req.Header.Set("If-Match", `"3"`)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("if-match status %d", res.StatusCode)
		}
	})

	t.Run("06_group_message_mentions_and_refs", func(t *testing.T) {
		members := agentA.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/members", nil, http.StatusOK)
		var bID string
		for _, item := range arr(t, members, "members") {
			member := item.(map[string]any)
			if str(member, "display_name") == "Product Agent" {
				bID = str(member, "agent_id")
			}
		}
		if bID == "" {
			t.Fatal(members)
		}
		msg := agentA.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages", map[string]any{
			"content_md":      "请看排障记录",
			"mentions":        []string{bID},
			"document_refs":   []map[string]any{{"document_id": docID, "revision": 2}},
			"asset_ids":       []string{assetID},
			"idempotency_key": "group-msg-1",
		}, http.StatusCreated)
		if str(msg, "id") == "" {
			t.Fatal(msg)
		}
		page := agentB.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages?q="+urlQuery("排障"), nil, http.StatusOK)
		messages := arr(t, page, "messages")
		if len(messages) != 1 {
			t.Fatalf("messages %#v", page)
		}
		got := messages[0].(map[string]any)
		mentions := got["mentions"].([]any)
		if len(mentions) != 1 || mentions[0].(string) != bID {
			t.Fatal(got)
		}
		refs := got["document_refs"].([]any)
		if len(refs) != 1 || num(refs[0].(map[string]any), "revision") != 2 {
			t.Fatal(got)
		}
	})

	t.Run("07_dm_hidden_from_third_agent_and_admin", func(t *testing.T) {
		agentC = join(t, e.srv.URL, inviteURL, "Deploy Agent", "custom", "laptop-deploy-01")
		members := agentA.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/members", nil, http.StatusOK)
		var bID string
		for _, item := range arr(t, members, "members") {
			member := item.(map[string]any)
			if str(member, "display_name") == "Product Agent" {
				bID = str(member, "agent_id")
			}
		}
		room := agentA.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/dms", map[string]any{"peer_agent_id": bID}, http.StatusOK)
		dmRoom = str(room, "id")
		agentA.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/rooms/"+dmRoom+"/messages", map[string]any{
			"content_md": "只有 A 和 B 能看到的句子 secret-dm-phrase",
		}, http.StatusCreated)
		agentB.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/rooms/"+dmRoom+"/messages", nil, http.StatusOK)
		denied := agentC.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/rooms/"+dmRoom+"/messages", nil, http.StatusForbidden)
		if str(obj(t, denied, "error"), "code") != "FORBIDDEN" || strings.Contains(string(mustJSON(denied)), "secret-dm-phrase") {
			t.Fatal(denied)
		}
		adminDenied := e.admin.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/rooms/"+dmRoom+"/messages", nil, http.StatusForbidden)
		if strings.Contains(string(mustJSON(adminDenied)), "secret-dm-phrase") {
			t.Fatal(adminDenied)
		}
		adminRooms := e.admin.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/rooms", nil, http.StatusOK)
		for _, item := range arr(t, adminRooms, "rooms") {
			if str(item.(map[string]any), "id") == dmRoom {
				t.Fatal("admin room list exposed a DM")
			}
		}
		events := agentC.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/events?after_seq=0&limit=200", nil, http.StatusOK)
		if strings.Contains(string(mustJSON(events)), "secret-dm-phrase") || strings.Contains(string(mustJSON(events)), dmRoom) {
			t.Fatalf("dm leaked in events %#v", events)
		}
		tool := mcp(t, e.srv.URL, agentC.token, "tools/call", map[string]any{
			"name": "chat_list_messages", "arguments": map[string]any{"room_id": dmRoom},
		})
		if tool["isError"] != true {
			t.Fatalf("mcp dm %#v", tool)
		}
	})

	t.Run("08_cursor_recovers_without_full_history", func(t *testing.T) {
		before := agentB.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/events?after_seq=0&limit=200", nil, http.StatusOK)
		cursor = num(before, "next_seq")
		if cursor == 0 {
			t.Fatal(before)
		}
		agentA.call(t, http.MethodPatch, "/api/v1/spaces/"+spaceID+"/documents/"+docID, map[string]any{
			"content_md": "# 离线期间的新结论", "expected_revision": 4, "change_note": "离线补充",
		}, http.StatusOK)
		agentA.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages", map[string]any{
			"content_md": "B 离线时的新消息",
		}, http.StatusCreated)
		delta := agentB.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/events?after_seq="+itoa(cursor)+"&limit=200", nil, http.StatusOK)
		events := arr(t, delta, "events")
		if len(events) < 2 {
			t.Fatalf("delta %#v", delta)
		}
		for _, item := range events {
			if num(item.(map[string]any), "seq") <= cursor {
				t.Fatalf("event did not advance past cursor %#v", item)
			}
		}
		if num(delta, "next_seq") <= cursor {
			t.Fatal(delta)
		}
		again := agentB.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/events?after_seq="+itoa(num(delta, "next_seq")), nil, http.StatusOK)
		if len(arr(t, again, "events")) != 0 {
			t.Fatalf("expected empty %#v", again)
		}
		ack := agentB.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/events/ack", map[string]any{"last_ack_seq": num(delta, "next_seq")}, http.StatusOK)
		if num(ack, "last_ack_seq") != num(delta, "next_seq") {
			t.Fatal(ack)
		}
		e.admin.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/events/ack", map[string]any{"last_ack_seq": 0}, http.StatusForbidden)
	})

	t.Run("09_idempotent_message", func(t *testing.T) {
		body := map[string]any{"content_md": "同一条重试", "idempotency_key": "retry-key-9"}
		first := agentA.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages", body, http.StatusCreated)
		second := agentA.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages", body, http.StatusOK)
		if str(first, "id") != str(second, "id") || second["replayed"] != true {
			t.Fatalf("first %#v second %#v", first, second)
		}
		different := map[string]any{"content_md": "不同内容", "idempotency_key": "retry-key-9"}
		agentA.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages", different, http.StatusConflict)
		page := agentA.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages?q="+urlQuery("同一条重试"), nil, http.StatusOK)
		if len(arr(t, page, "messages")) != 1 {
			t.Fatal(page)
		}
	})

	t.Run("10_revoke_invite_keeps_members", func(t *testing.T) {
		e.admin.call(t, http.MethodDelete, "/api/v1/spaces/"+spaceID+"/invitations/"+inviteID, nil, http.StatusOK)
		res, err := http.Get(inviteURL + "/manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusGone {
			t.Fatalf("revoked manifest %d", res.StatusCode)
		}
		reqBody := []byte(`{"display_name":"Late","runtime":"HTTP","instance_id":"late-agent"}`)
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/v1/invitations/"+inviteCode+"/join", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		res, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusGone {
			t.Fatalf("revoked join %d", res.StatusCode)
		}
		agentA.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+docID, nil, http.StatusOK)
		listed := e.admin.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/invitations", nil, http.StatusOK)
		if strings.Contains(string(mustJSON(listed)), inviteCode) {
			t.Fatal("invitation list leaked the code")
		}
	})

	t.Run("11_revoke_member_token", func(t *testing.T) {
		members := e.admin.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/members", nil, http.StatusOK)
		var bID string
		for _, item := range arr(t, members, "members") {
			member := item.(map[string]any)
			if str(member, "display_name") == "Product Agent" {
				bID = str(member, "agent_id")
			}
		}
		e.admin.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/members/"+bID+"/revoke", nil, http.StatusOK)
		agentB.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+docID, nil, http.StatusUnauthorized)
		agentB.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages", nil, http.StatusUnauthorized)
		agentB.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/assets/"+assetID, nil, http.StatusUnauthorized)
		agentA.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+docID, nil, http.StatusOK)
		audit := e.admin.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/audit", nil, http.StatusOK)
		if strings.Contains(string(mustJSON(audit)), agentA.token) || strings.Contains(string(mustJSON(audit)), inviteCode) {
			t.Fatal("audit leaked a credential")
		}
	})

	t.Run("12_space_isolation", func(t *testing.T) {
		other := e.admin.call(t, http.MethodPost, "/api/v1/spaces", map[string]any{"name": "另一件事"}, http.StatusCreated)
		otherID := str(obj(t, other, "space"), "id")
		denied := agentA.call(t, http.MethodGet, "/api/v1/spaces/"+otherID+"/bootstrap", nil, http.StatusForbidden)
		if strings.Contains(string(mustJSON(denied)), "另一件事") {
			t.Fatal(denied)
		}
		agentA.call(t, http.MethodGet, "/api/v1/spaces/"+otherID+"/documents", nil, http.StatusForbidden)
		agentA.call(t, http.MethodGet, "/api/v1/spaces/"+otherID+"/assets/"+assetID, nil, http.StatusForbidden)
	})

	t.Run("14_mcp_matches_http", func(t *testing.T) {
		initResult := mcp(t, e.srv.URL, agentA.token, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "test", "version": "0"}})
		if str(obj(t, initResult, "serverInfo"), "name") != "hello-center" {
			t.Fatal(initResult)
		}
		listed := mcp(t, e.srv.URL, agentA.token, "tools/list", map[string]any{})
		names := map[string]bool{}
		for _, item := range arr(t, listed, "tools") {
			names[str(item.(map[string]any), "name")] = true
		}
		for _, name := range []string{"space_get_overview", "document_create", "document_update", "chat_send_message", "events_pull", "events_ack"} {
			if !names[name] {
				t.Fatalf("missing tool %s", name)
			}
		}
		created := mcp(t, e.srv.URL, agentA.token, "tools/call", map[string]any{
			"name":      "document_create",
			"arguments": map[string]any{"title": "MCP 记录", "content_md": "来自 MCP"},
		})
		if created["isError"] == true {
			t.Fatal(created)
		}
		structured := obj(t, created, "structuredContent")
		mcpDoc := str(structured, "id")
		got := agentC.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+mcpDoc, nil, http.StatusOK)
		if str(got, "content_md") != "来自 MCP" {
			t.Fatal(got)
		}
		sent := mcp(t, e.srv.URL, agentA.token, "tools/call", map[string]any{
			"name":      "chat_send_message",
			"arguments": map[string]any{"room_id": roomID, "content_md": "MCP 群聊", "idempotency_key": "mcp-msg-1"},
		})
		if sent["isError"] == true {
			t.Fatal(sent)
		}
		pulled := mcp(t, e.srv.URL, agentA.token, "tools/call", map[string]any{
			"name": "events_pull", "arguments": map[string]any{"after_seq": 0, "limit": 200},
		})
		if strings.Contains(string(mustJSON(pulled)), "secret-dm-phrase") {
			t.Fatal("mcp events leaked dm")
		}
		wrong := mcp(t, e.srv.URL, agentA.token, "tools/call", map[string]any{
			"name": "space_get_overview", "arguments": map[string]any{"space_id": "sp_other"},
		})
		if wrong["isError"] != true {
			t.Fatal(wrong)
		}
	})

	t.Run("15_http_without_mcp", func(t *testing.T) {
		rotated := e.admin.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/invitations/rotate", map[string]any{}, http.StatusOK)
		freshURL := str(rotated, "invite_url")
		httpAgent := join(t, e.srv.URL, freshURL, "HTTP Only", "curl", "http-only-01")
		boot := httpAgent.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/bootstrap", nil, http.StatusOK)
		if str(obj(t, boot, "space"), "id") != spaceID {
			t.Fatal(boot)
		}
		httpAgent.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+docID, nil, http.StatusOK)
		httpAgent.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages", map[string]any{
			"content_md": "只使用 HTTP",
		}, http.StatusCreated)
		pulled := httpAgent.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/events?after_seq=0&limit=50", nil, http.StatusOK)
		if num(pulled, "server_seq") == 0 {
			t.Fatal(pulled)
		}
	})

	t.Run("16_malicious_filename_and_markdown", func(t *testing.T) {
		upload(t, agentA, spaceID, "../../etc/passwd.png", tinyPNG(t), http.StatusCreated)
		upload(t, agentA, spaceID, "evil.html", tinyPNG(t), http.StatusBadRequest)
		upload(t, agentA, spaceID, "not-a-png.png", []byte("<script>alert(1)</script>"), http.StatusBadRequest)
		note := upload(t, agentA, spaceID, "trace.log", []byte("line with <script>alert(1)</script>\n"), http.StatusCreated)
		status, body, header := agentA.do(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/assets/"+str(note, "id"), nil, "")
		if status != http.StatusOK || !strings.Contains(header.Get("Content-Disposition"), "attachment") || header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("log download %d %#v", status, header)
		}
		if !bytes.Contains(body, []byte("<script>alert(1)</script>")) {
			t.Fatal("log body was altered")
		}
		err := filepath.WalkDir(e.dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.Contains(d.Name(), "passwd") || strings.Contains(d.Name(), "pwned") {
				t.Fatalf("unexpected path %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		nasty := e.admin.call(t, http.MethodPost, "/api/v1/spaces", map[string]any{
			"name": `<script>alert(1)</script>`,
		}, http.StatusCreated)
		page, err := http.Get(str(obj(t, nasty, "invitation"), "invite_url"))
		if err != nil {
			t.Fatal(err)
		}
		html, _ := io.ReadAll(page.Body)
		page.Body.Close()
		if bytes.Contains(html, []byte("<script>alert(1)</script>")) {
			t.Fatalf("invite page executed markup: %s", html)
		}
		if !bytes.Contains(html, []byte("&lt;script&gt;")) {
			t.Fatalf("invite page did not escape: %s", html)
		}
		stored := agentA.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/documents", map[string]any{
			"title": "外部内容", "content_md": "<script>alert(1)</script>\n[x](javascript:alert(1))",
		}, http.StatusCreated)
		if str(stored, "content_md") == "" {
			t.Fatal(stored)
		}
		loaded := agentC.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+str(stored, "id"), nil, http.StatusOK)
		if !strings.Contains(str(loaded, "content_md"), "<script>alert(1)</script>") {
			t.Fatal("markdown was rewritten instead of stored as data")
		}
	})

	t.Run("sse_recovers_new_event", func(t *testing.T) {
		boot := agentA.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/bootstrap", nil, http.StatusOK)
		seq := num(obj(t, boot, "event_cursor"), "server_seq")
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/api/v1/spaces/"+spaceID+"/events/stream?after_seq="+itoa(seq), nil)
		req.Header.Set("Authorization", "Bearer "+agentA.token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("sse status %d", res.StatusCode)
		}
		found := make(chan string, 1)
		go func() {
			reader := bufio.NewReader(res.Body)
			var collected strings.Builder
			for {
				line, err := reader.ReadString('\n')
				collected.WriteString(line)
				if strings.Contains(collected.String(), "document.created") && strings.Contains(collected.String(), "SSE") {
					found <- "ok"
					return
				}
				if err != nil {
					found <- err.Error()
					return
				}
			}
		}()
		time.Sleep(80 * time.Millisecond)
		agentC.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/documents", map[string]any{
			"title": "SSE 文档", "content_md": "stream",
		}, http.StatusCreated)
		select {
		case msg := <-found:
			if msg != "ok" {
				t.Fatalf("sse read: %s", msg)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("sse did not deliver the new document event")
		}
	})

	t.Run("message_pagination", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			agentA.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages", map[string]any{
				"content_md": "分页-" + itoa(int64(i)),
			}, http.StatusCreated)
		}
		page := agentA.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages?limit=2", nil, http.StatusOK)
		if len(arr(t, page, "messages")) != 2 || str(page, "next_cursor") == "" {
			t.Fatal(page)
		}
		older := agentA.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages?limit=2&cursor="+str(page, "next_cursor"), nil, http.StatusOK)
		if len(arr(t, older, "messages")) == 0 {
			t.Fatal(older)
		}
	})
}

func TestRestartPreservesData(t *testing.T) {
	dir := t.TempDir()
	e := startAt(t, dir, nil, -1)
	created := e.admin.call(t, http.MethodPost, "/api/v1/spaces", map[string]any{"name": "重启后仍在", "goal": "保留"}, http.StatusCreated)
	spaceID := str(obj(t, created, "space"), "id")
	roomID := str(created, "general_room_id")
	agent := join(t, e.srv.URL, str(obj(t, created, "invitation"), "invite_url"), "Restart Agent", "HTTP", "restart-1")
	doc := agent.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/documents", map[string]any{
		"title": "持久", "content_md": "第一版",
	}, http.StatusCreated)
	docID := str(doc, "id")
	agent.call(t, http.MethodPatch, "/api/v1/spaces/"+spaceID+"/documents/"+docID, map[string]any{
		"content_md": "第二版", "expected_revision": 1,
	}, http.StatusOK)
	agent.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages", map[string]any{
		"content_md": "重启前的消息",
	}, http.StatusCreated)
	e2 := e.restart(t)
	detail := e2.admin.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID, nil, http.StatusOK)
	if str(obj(t, detail, "space"), "goal") != "保留" {
		t.Fatal(detail)
	}
	// The old token is still in the database.
	agent.base = e2.srv.URL
	got := agent.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+docID, nil, http.StatusOK)
	if str(got, "content_md") != "第二版" || num(got, "revision") != 2 {
		t.Fatal(got)
	}
	history := agent.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+docID+"/revisions/1", nil, http.StatusOK)
	if str(history, "content_md") != "第一版" {
		t.Fatal(history)
	}
	page := agent.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages?q="+urlQuery("重启前"), nil, http.StatusOK)
	if len(arr(t, page, "messages")) != 1 {
		t.Fatal(page)
	}
	events := agent.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/events?after_seq=0", nil, http.StatusOK)
	if num(events, "server_seq") < 2 {
		t.Fatal(events)
	}
}

func TestJoinRateLimit(t *testing.T) {
	e := startAt(t, t.TempDir(), nil, 2)
	created := e.admin.call(t, http.MethodPost, "/api/v1/spaces", map[string]any{"name": "限流"}, http.StatusCreated)
	code := str(obj(t, created, "invitation"), "invite_code")
	for i := 0; i < 2; i++ {
		body := map[string]string{"display_name": "A", "runtime": "HTTP", "instance_id": "rate-" + itoa(int64(i))}
		status, raw, _ := (&api{base: e.srv.URL, http: http.DefaultClient}).do(t, http.MethodPost, "/api/v1/invitations/"+code+"/join", body, "")
		if status != http.StatusCreated {
			t.Fatalf("join %d %s", status, raw)
		}
	}
	body := map[string]string{"display_name": "A", "runtime": "HTTP", "instance_id": "rate-3"}
	status, raw, header := (&api{base: e.srv.URL, http: http.DefaultClient}).do(t, http.MethodPost, "/api/v1/invitations/"+code+"/join", body, "")
	if status != http.StatusTooManyRequests || header.Get("Retry-After") == "" {
		t.Fatalf("rate %d %s", status, raw)
	}
}

func TestLogsDoNotContainSecrets(t *testing.T) {
	logs := &bytes.Buffer{}
	e := startAt(t, t.TempDir(), logs, -1)
	created := e.admin.call(t, http.MethodPost, "/api/v1/spaces", map[string]any{"name": "日志"}, http.StatusCreated)
	inv := obj(t, created, "invitation")
	agent := join(t, e.srv.URL, str(inv, "invite_url"), "Log Agent", "HTTP", "log-1")
	if strings.Contains(logs.String(), str(inv, "invite_code")) || strings.Contains(logs.String(), agent.token) {
		t.Fatalf("logs leaked credentials: %s", logs.String())
	}
}

func TestFinalDemoPath(t *testing.T) {
	dir := t.TempDir()
	e := startAt(t, dir, nil, -1)
	created := e.admin.call(t, http.MethodPost, "/api/v1/spaces", map[string]any{
		"name": "启动失败排查", "goal": "协调 Coding Agent 与 Deployment Agent",
	}, http.StatusCreated)
	spaceID := str(obj(t, created, "space"), "id")
	invite := str(obj(t, created, "invitation"), "invite_url")
	roomID := str(created, "general_room_id")
	coding := join(t, e.srv.URL, invite, "Coding Agent", "Codex CLI", "demo-coding")
	deploy := join(t, e.srv.URL, invite, "Deployment Agent", "custom", "demo-deploy")
	doc := coding.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/documents", map[string]any{
		"title": "启动日志摘要", "content_md": "# 现象\n\n进程退出码 1。",
	}, http.StatusCreated)
	asset := upload(t, coding, spaceID, "startup.log", []byte("exit status 1\n"), http.StatusCreated)
	deploy.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+str(doc, "id"), nil, http.StatusOK)
	status, body, _ := deploy.do(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/assets/"+str(asset, "id"), nil, "")
	if status != http.StatusOK || !bytes.Contains(body, []byte("exit status 1")) {
		t.Fatalf("asset %d %s", status, body)
	}
	members := deploy.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/members", nil, http.StatusOK)
	var codingID string
	for _, item := range arr(t, members, "members") {
		member := item.(map[string]any)
		if str(member, "display_name") == "Coding Agent" {
			codingID = str(member, "agent_id")
		}
	}
	deploy.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages", map[string]any{
		"content_md":    "日志已上传，请看文档。",
		"mentions":      []string{codingID},
		"document_refs": []map[string]any{{"document_id": str(doc, "id")}},
	}, http.StatusCreated)
	dm := coding.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/dms", map[string]any{
		"peer_agent_id": memberID(t, members, "Deployment Agent"),
	}, http.StatusOK)
	coding.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/rooms/"+str(dm, "id")+"/messages", map[string]any{
		"content_md": "我先在这边核对启动参数。",
	}, http.StatusCreated)
	observer := join(t, e.srv.URL, invite, "Observer", "HTTP", "demo-observer")
	observer.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/rooms/"+str(dm, "id")+"/messages", nil, http.StatusForbidden)
	before := deploy.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/events?after_seq=0&limit=200", nil, http.StatusOK)
	cursor := num(before, "next_seq")
	coding.call(t, http.MethodPatch, "/api/v1/spaces/"+spaceID+"/documents/"+str(doc, "id"), map[string]any{
		"content_md": "# 现象\n\n缺少配置文件。", "expected_revision": 1,
	}, http.StatusOK)
	coding.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/rooms/"+roomID+"/messages", map[string]any{
		"content_md": "已更新结论。",
	}, http.StatusCreated)
	delta := deploy.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/events?after_seq="+itoa(cursor), nil, http.StatusOK)
	if len(arr(t, delta, "events")) < 2 {
		t.Fatal(delta)
	}
	rotated := e.admin.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/invitations/rotate", nil, http.StatusOK)
	old := str(obj(t, created, "invitation"), "invite_code")
	res, err := http.Get(e.srv.URL + "/i/" + old + "/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusGone {
		t.Fatalf("old invite %d", res.StatusCode)
	}
	fresh := join(t, e.srv.URL, str(rotated, "invite_url"), "New Agent", "HTTP", "demo-new")
	fresh.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/bootstrap", nil, http.StatusOK)
	e.admin.call(t, http.MethodPost, "/api/v1/spaces/"+spaceID+"/members/"+memberID(t, members, "Deployment Agent")+"/revoke", nil, http.StatusOK)
	deploy.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/bootstrap", nil, http.StatusUnauthorized)
	coding.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+str(doc, "id"), nil, http.StatusOK)
	e2 := e.restart(t)
	coding.base = e2.srv.URL
	got := coding.call(t, http.MethodGet, "/api/v1/spaces/"+spaceID+"/documents/"+str(doc, "id"), nil, http.StatusOK)
	if !strings.Contains(str(got, "content_md"), "缺少配置文件") {
		t.Fatal(got)
	}
	t.Log("demo path passed: invite, two agents, document, attachment, group reply, private chat isolation, cursor catch-up, invite rotation, token revoke, restart")
}

func memberID(t *testing.T, members map[string]any, name string) string {
	t.Helper()
	for _, item := range arr(t, members, "members") {
		member := item.(map[string]any)
		if str(member, "display_name") == name {
			return str(member, "agent_id")
		}
	}
	t.Fatalf("member %s not found", name)
	return ""
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func itoa(n int64) string {
	return strconvFormat(n)
}

func strconvFormat(n int64) string {
	return strings.TrimPrefix(strings.Replace(jsonNumber(n), ",", "", -1), "")
}

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func urlQuery(s string) string {
	return url.QueryEscape(s)
}
