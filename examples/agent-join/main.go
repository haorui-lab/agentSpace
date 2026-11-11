package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func main() {
	invite := strings.TrimSpace(os.Getenv("HELLO_CENTER_INVITE"))
	if invite == "" {
		fmt.Fprintln(os.Stderr, "请设置 HELLO_CENTER_INVITE 为邀请链接")
		os.Exit(2)
	}
	parsed, err := url.Parse(invite)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		fmt.Fprintln(os.Stderr, "邀请链接不完整")
		os.Exit(2)
	}
	code := strings.Trim(strings.TrimPrefix(parsed.EscapedPath(), "/i/"), "/")
	code, err = url.PathUnescape(code)
	if err != nil || code == "" || strings.Contains(code, "/") {
		fmt.Fprintln(os.Stderr, "邀请链接里没有邀请码")
		os.Exit(2)
	}
	base := parsed.Scheme + "://" + parsed.Host
	manifest, err := getJSON(base+"/i/"+url.PathEscape(code)+"/manifest.json", "")
	if err != nil {
		fail(err)
	}
	fmt.Printf("manifest schema %v\n", manifest["schema_version"])

	name := env("HELLO_CENTER_AGENT_NAME", "示例 Agent")
	body, err := postJSON(base+"/api/v1/invitations/"+url.PathEscape(code)+"/join", "", map[string]string{
		"display_name": name,
		"runtime":      env("HELLO_CENTER_RUNTIME", "example"),
		"instance_id":  env("HELLO_CENTER_INSTANCE", "example-1"),
		"owner_label":  env("HELLO_CENTER_OWNER", ""),
		"description":  "examples/agent-join",
	})
	if err != nil {
		fail(err)
	}
	token, _ := body["access_token"].(string)
	fmt.Printf("space_id %s\nagent_id %s\nreissued %v\n", body["space_id"], body["agent_id"], body["reissued"])
	fmt.Printf("access_token %s\n", token)
	fmt.Println("上面的访问令牌只打印这一次。之后用 Authorization: Bearer 调用 API 或 MCP。")

	boot, err := getJSON(fmt.Sprint(body["bootstrap_url"]), token)
	if err != nil {
		fail(err)
	}
	space, _ := boot["space"].(map[string]any)
	fmt.Printf("bootstrap space %v\n", space["name"])
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func getJSON(rawURL, token string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return do(req)
}

func postJSON(rawURL, token string, payload any) (map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return do(req)
}

func do(req *http.Request) (map[string]any, error) {
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d %s", res.StatusCode, strings.TrimSpace(string(data)))
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(1)
}
