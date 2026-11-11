#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
if [ -z "${ADMIN_PASSWORD:-}" ]; then
  echo "请先设置 ADMIN_PASSWORD" >&2
  exit 1
fi
export HELLO_CENTER_URL="${HELLO_CENTER_URL:-http://127.0.0.1:8080}"
export ADMIN_USERNAME="${ADMIN_USERNAME:-admin}"
export SEED_NAME="${1:-启动失败排查}"
python3 - <<'PY'
import json, os, urllib.request, urllib.error
base = os.environ["HELLO_CENTER_URL"].rstrip("/")
username = os.environ["ADMIN_USERNAME"]
password = os.environ["ADMIN_PASSWORD"]
name = os.environ["SEED_NAME"]

def request(method, path, payload=None, cookie=""):
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(base + path, data=data, method=method)
    req.add_header("X-Hello-Center", "1")
    if payload is not None:
        req.add_header("Content-Type", "application/json")
    if cookie:
        req.add_header("Cookie", cookie)
    try:
        with urllib.request.urlopen(req) as res:
            body = res.read().decode()
            jar = res.headers.get_all("Set-Cookie") or []
    except urllib.error.HTTPError as err:
        raise SystemExit(err.read().decode())
    session = cookie
    for item in jar:
        if item.startswith("hc_session="):
            session = item.split(";", 1)[0]
    return session, json.loads(body)

session, _ = request("POST", "/api/v1/admin/login", {"username": username, "password": password})
_, created = request("POST", "/api/v1/spaces", {"name": name, "goal": "记录启动失败的线索"}, session)
print(json.dumps({
    "space_id": created["space"]["id"],
    "invite_url": created["invitation"]["invite_url"],
}, ensure_ascii=False, indent=2))
print("邀请链接只在这次输出里出现。")
PY
