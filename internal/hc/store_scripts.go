package hc

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// Lua key layout matches store.key. Event JSON always starts with {"seq":0 so
// the script can stamp the allocated sequence without re-encoding user text.
const luaHeader = `
local function touch_space(prefix, space, ts, score)
  local key = prefix .. ':space:' .. space
  local sp = redis.call('GET', key)
  if not sp then return end
  local touched, n = string.gsub(sp, '^({"updated_at":")[^"]*"', '%1' .. ts .. '"', 1)
  if n == 1 then sp = touched end
  redis.call('SET', key, sp)
  local owner = string.match(sp, '"owner_id":"([^"]*)"')
  if owner and owner ~= '' then
    redis.call('ZADD', prefix .. ':owner:' .. owner .. ':spaces', score, space)
  end
end

local function push_event(prefix, space, evjson, ts, score)
  if string.sub(evjson, 1, 7) ~= '{"seq":' then
    return redis.error_reply('event json must start with seq')
  end
  local seq = redis.call('INCR', prefix .. ':space:' .. space .. ':seq')
  local seqs = string.format('%.0f', seq)
  local encoded, n = string.gsub(evjson, '^({"seq":)%d+', '%1' .. seqs, 1)
  if n ~= 1 then
    return redis.error_reply('event seq was not stamped')
  end
  redis.call('SET', prefix .. ':event:' .. space .. ':' .. seqs, encoded)
  redis.call('ZADD', prefix .. ':events:' .. space, seq, seqs)
  touch_space(prefix, space, ts, score)
  return encoded
end

local function write_audit(prefix, auditjson, score)
  if not auditjson or auditjson == '' then return end
  local id = string.match(auditjson, '"id":"([^"]+)"')
  if not id then return end
  local space = string.match(auditjson, '"space_id":"([^"]*)"')
  redis.call('SET', prefix .. ':audit:' .. id, auditjson)
  if space and space ~= '' then
    redis.call('ZADD', prefix .. ':space:' .. space .. ':audit', score, id)
  end
end
`

var (
	scriptEnsureUser = redis.NewScript(luaHeader + `
local ready, userKey, nameKey, userJSON, userID = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5]
if redis.call('EXISTS', userKey) == 1 or redis.call('EXISTS', nameKey) == 1 then
  return {'exists'}
end
if redis.call('SETNX', ready, userID) == 0 then
  return {'exists'}
end
redis.call('SET', userKey, userJSON)
redis.call('SET', nameKey, userID)
return {'created'}
`)

	scriptCreateSpace = redis.NewScript(luaHeader + `
local prefix, spaceJSON, spaceID, ownerID, score = ARGV[1], ARGV[2], ARGV[3], ARGV[4], tonumber(ARGV[5])
local roomJSON, roomID = ARGV[6], ARGV[7]
local inviteJSON, inviteID, codeHash = ARGV[8], ARGV[9], ARGV[10]
local auditSpace, auditInvite = ARGV[11], ARGV[12]
redis.call('SET', prefix .. ':space:' .. spaceID, spaceJSON)
redis.call('ZADD', prefix .. ':owner:' .. ownerID .. ':spaces', score, spaceID)
redis.call('SET', prefix .. ':space:' .. spaceID .. ':seq', '0')
redis.call('SET', prefix .. ':room:' .. roomID, roomJSON)
redis.call('SET', prefix .. ':space:' .. spaceID .. ':general', roomID)
redis.call('SADD', prefix .. ':space:' .. spaceID .. ':rooms', roomID)
redis.call('SET', prefix .. ':invite:' .. inviteID, inviteJSON)
redis.call('SET', prefix .. ':invite:code:' .. codeHash, inviteID)
redis.call('SET', prefix .. ':space:' .. spaceID .. ':active_invite:' .. ownerID, inviteID)
redis.call('ZADD', prefix .. ':space:' .. spaceID .. ':invites', score, inviteID)
write_audit(prefix, auditSpace, score)
write_audit(prefix, auditInvite, score)
return {'ok'}
`)

	scriptPutInvite = redis.NewScript(luaHeader + `
local prefix, spaceID, ownerID, userID = ARGV[1], ARGV[2], ARGV[3], ARGV[4]
local inviteJSON, inviteID, codeHash = ARGV[5], ARGV[6], ARGV[7]
local score, ts, rotate, auditJSON = tonumber(ARGV[8]), ARGV[9], ARGV[10], ARGV[11]
local userKey = prefix .. ':space:' .. spaceID .. ':active_invite:' .. userID
local legacyKey = prefix .. ':space:' .. spaceID .. ':active_invite'
local active = redis.call('GET', userKey)
if (not active or active == '') then
  local legacy = redis.call('GET', legacyKey)
  if legacy and legacy ~= '' then
    local raw = redis.call('GET', prefix .. ':invite:' .. legacy)
    if raw then
      local created = string.match(raw, '"created_by":"([^"]*)"') or ''
      if created == userID or (created == '' and userID == ownerID) then
        active = legacy
      end
    end
  end
end
if active and active ~= '' then
  if rotate ~= '1' then
    return {'exists', active}
  end
  local raw = redis.call('GET', prefix .. ':invite:' .. active)
  if raw then
    raw = string.gsub(raw, '("status":")[^"]*"', '%1revoked"', 1)
    raw = string.gsub(raw, '("revoked_at":")[^"]*"', '%1' .. ts .. '"', 1)
    redis.call('SET', prefix .. ':invite:' .. active, raw)
  end
  if redis.call('GET', legacyKey) == active then
    redis.call('DEL', legacyKey)
  end
end
redis.call('SET', prefix .. ':invite:' .. inviteID, inviteJSON)
redis.call('SET', prefix .. ':invite:code:' .. codeHash, inviteID)
redis.call('SET', userKey, inviteID)
redis.call('ZADD', prefix .. ':space:' .. spaceID .. ':invites', score, inviteID)
write_audit(prefix, auditJSON, score)
return {'ok'}
`)

	scriptRevokeInvite = redis.NewScript(luaHeader + `
local prefix, spaceID, inviteID, ts, auditJSON, score = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5], tonumber(ARGV[6])
local userID, ownerID = ARGV[7], ARGV[8]
local key = prefix .. ':invite:' .. inviteID
local raw = redis.call('GET', key)
if not raw then return {'missing'} end
local space = string.match(raw, '"space_id":"([^"]+)"')
if space ~= spaceID then return {'missing'} end
local created = string.match(raw, '"created_by":"([^"]*)"') or ''
if created ~= '' and created ~= userID then
  return {'missing'}
end
if created == '' and userID ~= ownerID then
  return {'missing'}
end
local status = string.match(raw, '"status":"([^"]+)"')
local function clear_active(k)
  if redis.call('GET', k) == inviteID then
    redis.call('DEL', k)
  end
end
clear_active(prefix .. ':space:' .. spaceID .. ':active_invite')
if userID ~= '' then
  clear_active(prefix .. ':space:' .. spaceID .. ':active_invite:' .. userID)
end
if created ~= '' then
  clear_active(prefix .. ':space:' .. spaceID .. ':active_invite:' .. created)
end
if status == 'revoked' then
  return {'same', raw}
end
raw = string.gsub(raw, '("status":")[^"]*"', '%1revoked"', 1)
raw = string.gsub(raw, '("revoked_at":")[^"]*"', '%1' .. ts .. '"', 1)
redis.call('SET', key, raw)
write_audit(prefix, auditJSON, score)
return {'changed', raw}
`)

	scriptJoin = redis.NewScript(luaHeader + `
local prefix, spaceID, instance = ARGV[1], ARGV[2], ARGV[3]
local agentJSON, tokenHash, ts, score = ARGV[4], ARGV[5], ARGV[6], tonumber(ARGV[7])
local joinedSummary, refreshSummary, auditID = ARGV[8], ARGV[9], ARGV[10]
local invitationID, invitedBy = ARGV[11], ARGV[12]
local instKey = prefix .. ':agent:instance:' .. instance
local agentID = redis.call('GET', instKey)
if not agentID or agentID == '' then
  agentID = string.match(agentJSON, '"id":"([^"]+)"')
  redis.call('SET', instKey, agentID)
else
  local old = redis.call('GET', prefix .. ':agent:' .. agentID)
  local created = ts
  if old then
    local found = string.match(old, '"created_at":"([^"]+)"')
    if found then created = found end
  end
  agentJSON = string.gsub(agentJSON, '^({"id":")[^"]*"', '%1' .. agentID .. '"', 1)
  agentJSON = string.gsub(agentJSON, '("created_at":")[^"]*"', '%1' .. created .. '"', 1)
end
redis.call('SET', prefix .. ':agent:' .. agentID, agentJSON)
redis.call('SET', prefix .. ':seen:' .. agentID, ts)
local memberKey = prefix .. ':member:' .. spaceID .. ':' .. agentID
local oldMember = redis.call('GET', memberKey)
local joined = ts
local reissued = '0'
if oldMember then
  reissued = '1'
  local found = string.match(oldMember, '"joined_at":"([^"]+)"')
  if found then joined = found end
  local oldHash = string.match(oldMember, '"token_hash":"([^"]*)"')
  if oldHash and oldHash ~= '' then
    redis.call('DEL', prefix .. ':token:' .. oldHash)
  end
  local oldInv = string.match(oldMember, '"invitation_id":"([^"]*)"')
  local oldBy = string.match(oldMember, '"invited_by":"([^"]*)"')
  if oldInv and oldInv ~= '' then invitationID = oldInv end
  if oldBy and oldBy ~= '' then invitedBy = oldBy end
end
local memberJSON = '{"joined_at":"' .. joined .. '","space_id":"' .. spaceID .. '","agent_id":"' .. agentID .. '","status":"active","token_hash":"' .. tokenHash .. '","revoked_at":"","invitation_id":"' .. invitationID .. '","invited_by":"' .. invitedBy .. '"}'
redis.call('SET', memberKey, memberJSON)
redis.call('SET', prefix .. ':token:' .. tokenHash, spaceID .. '|' .. agentID)
redis.call('ZADD', prefix .. ':space:' .. spaceID .. ':members', 'NX', score, agentID)
local evType = 'member.joined'
local summary = joinedSummary
local action = 'member.join'
if reissued == '1' then
  evType = 'member.token_refreshed'
  summary = refreshSummary
  action = 'member.token_refresh'
end
local evjson = '{"seq":0,"type":"' .. evType .. '","resource_id":"' .. agentID .. '","actor_id":"' .. agentID .. '","summary":' .. summary .. ',"created_at":"' .. ts .. '","visibility":"space","space_id":"' .. spaceID .. '"}'
local encoded = push_event(prefix, spaceID, evjson, ts, score)
local audit = '{"id":"' .. auditID .. '","space_id":"' .. spaceID .. '","actor_id":"' .. agentID .. '","action":"' .. action .. '","object_type":"membership","object_id":"' .. agentID .. '","result":"ok","created_at":"' .. ts .. '"}'
write_audit(prefix, audit, score)
return {'ok', agentID, reissued, encoded}
`)

	scriptRevokeMember = redis.NewScript(luaHeader + `
local prefix, spaceID, agentID, ts, score, actorID, auditID = ARGV[1], ARGV[2], ARGV[3], ARGV[4], tonumber(ARGV[5]), ARGV[6], ARGV[7]
local key = prefix .. ':member:' .. spaceID .. ':' .. agentID
local raw = redis.call('GET', key)
if not raw then return {'missing'} end
local status = string.match(raw, '"status":"([^"]+)"')
if status ~= 'active' then return {'inactive'} end
local hash = string.match(raw, '"token_hash":"([^"]*)"')
if hash and hash ~= '' then
  redis.call('DEL', prefix .. ':token:' .. hash)
end
raw = string.gsub(raw, '("status":")[^"]*"', '%1revoked"', 1)
raw = string.gsub(raw, '("token_hash":")[^"]*"', '%1"', 1)
raw = string.gsub(raw, '("revoked_at":")[^"]*"', '%1' .. ts .. '"', 1)
redis.call('SET', key, raw)
local evjson = '{"seq":0,"type":"member.revoked","resource_id":"' .. agentID .. '","actor_id":"' .. actorID .. '","summary":"membership revoked","created_at":"' .. ts .. '","visibility":"space","space_id":"' .. spaceID .. '"}'
local encoded = push_event(prefix, spaceID, evjson, ts, score)
local audit = '{"id":"' .. auditID .. '","space_id":"' .. spaceID .. '","actor_id":"' .. actorID .. '","action":"member.revoke","object_type":"membership","object_id":"' .. agentID .. '","result":"ok","created_at":"' .. ts .. '"}'
write_audit(prefix, audit, score)
return {'revoked', encoded}
`)

	scriptCreateDoc = redis.NewScript(luaHeader + `
local prefix, spaceID, docID = ARGV[1], ARGV[2], ARGV[3]
local docJSON, revJSON, evJSON, auditJSON, ts, score = ARGV[4], ARGV[5], ARGV[6], ARGV[7], ARGV[8], tonumber(ARGV[9])
redis.call('SET', prefix .. ':doc:' .. docID, docJSON)
redis.call('SET', prefix .. ':doc:' .. docID .. ':rev:1', revJSON)
redis.call('SADD', prefix .. ':doc:' .. docID .. ':revs', '1')
redis.call('SADD', prefix .. ':space:' .. spaceID .. ':docs', docID)
local encoded = push_event(prefix, spaceID, evJSON, ts, score)
write_audit(prefix, auditJSON, score)
return {'ok', encoded}
`)

	scriptUpdateDoc = redis.NewScript(luaHeader + `
local prefix, spaceID, docID, expected = ARGV[1], ARGV[2], ARGV[3], ARGV[4]
local docJSON, revJSON, revNum = ARGV[5], ARGV[6], ARGV[7]
local evJSON, auditJSON, ts, score = ARGV[8], ARGV[9], ARGV[10], tonumber(ARGV[11])
local docKey = prefix .. ':doc:' .. docID
local raw = redis.call('GET', docKey)
if not raw then return {'missing'} end
if redis.call('EXISTS', prefix .. ':doc:' .. docID .. ':deleted') == 1 then return {'missing'} end
local space = string.match(raw, '"space_id":"([^"]+)"')
if space ~= spaceID then return {'missing'} end
local rev = string.match(raw, '"revision":(%d+)')
if rev ~= expected then return {'conflict', raw} end
redis.call('SET', docKey, docJSON)
redis.call('SET', prefix .. ':doc:' .. docID .. ':rev:' .. revNum, revJSON)
redis.call('SADD', prefix .. ':doc:' .. docID .. ':revs', revNum)
local encoded = push_event(prefix, spaceID, evJSON, ts, score)
write_audit(prefix, auditJSON, score)
return {'ok', encoded}
`)

	scriptDeleteDoc = redis.NewScript(luaHeader + `
local prefix, spaceID, docID = ARGV[1], ARGV[2], ARGV[3]
local evJSON, auditJSON, ts, score = ARGV[4], ARGV[5], ARGV[6], tonumber(ARGV[7])
local docKey = prefix .. ':doc:' .. docID
local raw = redis.call('GET', docKey)
if not raw then return {'missing'} end
if redis.call('EXISTS', prefix .. ':doc:' .. docID .. ':deleted') == 1 then return {'missing'} end
local space = string.match(raw, '"space_id":"([^"]+)"')
if space ~= spaceID then return {'missing'} end
redis.call('SET', prefix .. ':doc:' .. docID .. ':deleted', ts)
redis.call('SREM', prefix .. ':space:' .. spaceID .. ':docs', docID)
local encoded = push_event(prefix, spaceID, evJSON, ts, score)
write_audit(prefix, auditJSON, score)
return {'ok', encoded}
`)

	scriptCreateAsset = redis.NewScript(luaHeader + `
local prefix, spaceID, assetID = ARGV[1], ARGV[2], ARGV[3]
local assetJSON, evJSON, auditJSON, ts, score = ARGV[4], ARGV[5], ARGV[6], ARGV[7], tonumber(ARGV[8])
redis.call('SET', prefix .. ':asset:' .. assetID, assetJSON)
redis.call('SADD', prefix .. ':space:' .. spaceID .. ':assets', assetID)
local encoded = push_event(prefix, spaceID, evJSON, ts, score)
write_audit(prefix, auditJSON, score)
return {'ok', encoded}
`)

	scriptOpenDM = redis.NewScript(luaHeader + `
local prefix, spaceID, dmKey = ARGV[1], ARGV[2], ARGV[3]
local roomJSON, roomID, agentA, agentB = ARGV[4], ARGV[5], ARGV[6], ARGV[7]
local evJSON, ts, score = ARGV[8], ARGV[9], tonumber(ARGV[10])
local idxKey = prefix .. ':space:' .. spaceID .. ':dm:' .. dmKey
local existing = redis.call('GET', idxKey)
if existing and existing ~= '' then
  return {'exists', existing}
end
redis.call('SET', prefix .. ':room:' .. roomID, roomJSON)
redis.call('SET', idxKey, roomID)
redis.call('SADD', prefix .. ':space:' .. spaceID .. ':rooms', roomID)
redis.call('SADD', prefix .. ':agent:' .. agentA .. ':rooms:' .. spaceID, roomID)
redis.call('SADD', prefix .. ':agent:' .. agentB .. ':rooms:' .. spaceID, roomID)
local encoded = push_event(prefix, spaceID, evJSON, ts, score)
return {'created', encoded}
`)

	scriptSendMessage = redis.NewScript(luaHeader + `
local prefix, roomID, msgJSON, msgID = ARGV[1], ARGV[2], ARGV[3], ARGV[4]
local idemKey, replyID = ARGV[5], ARGV[6]
local evJSON, auditJSON, spaceID, ts, score = ARGV[7], ARGV[8], ARGV[9], ARGV[10], tonumber(ARGV[11])
if idemKey ~= '' then
  local existing = redis.call('GET', idemKey)
  if existing and existing ~= '' then
    return {'replay', existing}
  end
end
if replyID ~= '' then
  local raw = redis.call('GET', prefix .. ':msg:' .. replyID)
  if not raw then return {'badreply'} end
  local room = string.match(raw, '"room_id":"([^"]+)"')
  if room ~= roomID then return {'badreply'} end
end
local msgSeq = redis.call('INCR', prefix .. ':room:' .. roomID .. ':msgseq')
redis.call('SET', prefix .. ':msg:' .. msgID, msgJSON)
redis.call('ZADD', prefix .. ':room:' .. roomID .. ':msgs', msgSeq, msgID)
if idemKey ~= '' then
  redis.call('SET', idemKey, msgID)
end
local encoded = push_event(prefix, spaceID, evJSON, ts, score)
write_audit(prefix, auditJSON, score)
return {'ok', encoded}
`)

	scriptAck = redis.NewScript(luaHeader + `
local prefix, spaceID, agentID, last = ARGV[1], ARGV[2], ARGV[3], ARGV[4]
local auditJSON, score = ARGV[5], tonumber(ARGV[6])
if redis.call('EXISTS', prefix .. ':space:' .. spaceID) == 0 then
  return {'nospace'}
end
local seq = tonumber(redis.call('GET', prefix .. ':space:' .. spaceID .. ':seq') or '0')
if tonumber(last) > seq then
  return {'ahead'}
end
redis.call('SET', prefix .. ':cursor:' .. spaceID .. ':' .. agentID, last)
write_audit(prefix, auditJSON, score)
return {'ok'}
`)
)

func (st *store) ensureUser(ctx context.Context, user userRecord) (bool, error) {
	raw, err := encodeJSON(user)
	if err != nil {
		return false, err
	}
	arr, err := st.run(ctx, scriptEnsureUser, st.key("admin", "ready"), st.key("user", "id", user.ID), st.key("user", "name", user.Username), raw, user.ID)
	if err != nil {
		return false, err
	}
	return strAt(arr, 0) == "created", nil
}

func (st *store) createSpace(ctx context.Context, sp spaceRecord, room roomRecord, inv inviteRecord, actor string) error {
	if sp.CreatedAt == "" || sp.UpdatedAt == "" {
		ts, _ := stamp()
		if sp.CreatedAt == "" {
			sp.CreatedAt = ts
		}
		if sp.UpdatedAt == "" {
			sp.UpdatedAt = ts
		}
	}
	if room.CreatedAt == "" {
		room.CreatedAt = sp.CreatedAt
	}
	if room.MemberIDs == nil {
		room.MemberIDs = []string{}
	}
	if inv.CreatedAt == "" {
		inv.CreatedAt = sp.CreatedAt
	}
	ts := sp.UpdatedAt
	score := scoreOf(ts)
	spaceJSON, err := encodeJSON(sp)
	if err != nil {
		return err
	}
	roomJSON, err := encodeJSON(room)
	if err != nil {
		return err
	}
	inviteJSON, err := encodeJSON(inv)
	if err != nil {
		return err
	}
	auditSpace, err := encodeJSON(newAudit(sp.ID, actor, "space.create", "space", sp.ID, "ok", ts))
	if err != nil {
		return err
	}
	auditInvite, err := encodeJSON(newAudit(sp.ID, actor, "invitation.create", "invitation", inv.ID, "ok", ts))
	if err != nil {
		return err
	}
	_, err = st.run(ctx, scriptCreateSpace, st.prefix, spaceJSON, sp.ID, sp.OwnerID, score, roomJSON, room.ID, inviteJSON, inv.ID, inv.CodeHash, auditSpace, auditInvite)
	return err
}

func (st *store) putInvite(ctx context.Context, inv inviteRecord, actor, action, ownerID string, rotate bool) error {
	if inv.CreatedAt == "" {
		inv.CreatedAt, _ = stamp()
	}
	if inv.CreatedBy == "" {
		inv.CreatedBy = actor
	}
	ts := inv.CreatedAt
	score := scoreOf(ts)
	raw, err := encodeJSON(inv)
	if err != nil {
		return err
	}
	audit, err := encodeJSON(newAudit(inv.SpaceID, actor, action, "invitation", inv.ID, "ok", ts))
	if err != nil {
		return err
	}
	flag := "0"
	if rotate {
		flag = "1"
	}
	if ownerID == "" {
		ownerID = inv.CreatedBy
	}
	arr, err := st.run(ctx, scriptPutInvite, st.prefix, inv.SpaceID, ownerID, inv.CreatedBy, raw, inv.ID, inv.CodeHash, score, ts, flag, audit)
	if err != nil {
		return err
	}
	if strAt(arr, 0) == "exists" {
		return conflict("an active invitation already exists; rotate it", map[string]any{"invitation_id": strAt(arr, 1)})
	}
	return nil
}

func (st *store) revokeInvite(ctx context.Context, space, inviteID, actor, ownerID string) (inviteRecord, bool, error) {
	ts, score := stamp()
	audit, err := encodeJSON(newAudit(space, actor, "invitation.revoke", "invitation", inviteID, "ok", ts))
	if err != nil {
		return inviteRecord{}, false, err
	}
	arr, err := st.run(ctx, scriptRevokeInvite, st.prefix, space, inviteID, ts, audit, score, actor, ownerID)
	if err != nil {
		return inviteRecord{}, false, err
	}
	switch strAt(arr, 0) {
	case "missing":
		return inviteRecord{}, false, notFound("invitation not found")
	case "same", "changed":
		var inv inviteRecord
		if err := json.Unmarshal([]byte(strAt(arr, 1)), &inv); err != nil {
			return inviteRecord{}, false, err
		}
		return inv, strAt(arr, 0) == "changed", nil
	default:
		return inviteRecord{}, false, internalf("unexpected invite result")
	}
}

type joinProfile struct {
	DisplayName  string
	Runtime      string
	InstanceID   string
	OwnerLabel   string
	Description  string
	TokenHash    string
	NewAgentID   string
	InvitationID string
	InvitedBy    string
}

func (st *store) join(ctx context.Context, spaceID string, profile joinProfile) (string, bool, Event, error) {
	ts, score := stamp()
	agent := agentRecord{
		ID: profile.NewAgentID, CreatedAt: ts, InstanceID: profile.InstanceID, DisplayName: profile.DisplayName,
		Runtime: profile.Runtime, OwnerLabel: profile.OwnerLabel, Description: profile.Description,
	}
	agentJSON, err := encodeJSON(agent)
	if err != nil {
		return "", false, Event{}, err
	}
	joined, err := json.Marshal(trimSummary(profile.DisplayName + " joined"))
	if err != nil {
		return "", false, Event{}, err
	}
	refreshed, err := json.Marshal(trimSummary(profile.DisplayName + " refreshed credentials"))
	if err != nil {
		return "", false, Event{}, err
	}
	arr, err := st.run(ctx, scriptJoin, st.prefix, spaceID, profile.InstanceID, agentJSON, profile.TokenHash, ts, score, string(joined), string(refreshed), newID("aud_"), profile.InvitationID, profile.InvitedBy)
	if err != nil {
		return "", false, Event{}, err
	}
	if strAt(arr, 0) != "ok" {
		return "", false, Event{}, internalf("unexpected join result")
	}
	ev, err := decodeEvent(strAt(arr, 3))
	if err != nil {
		return "", false, Event{}, err
	}
	return strAt(arr, 1), strAt(arr, 2) == "1", ev, nil
}

func (st *store) revokeMember(ctx context.Context, space, agent, actor string) (Event, bool, error) {
	ts, score := stamp()
	arr, err := st.run(ctx, scriptRevokeMember, st.prefix, space, agent, ts, score, actor, newID("aud_"))
	if err != nil {
		return Event{}, false, err
	}
	switch strAt(arr, 0) {
	case "missing":
		return Event{}, false, notFound("member not found")
	case "inactive":
		return Event{}, false, nil
	case "revoked":
		ev, err := decodeEvent(strAt(arr, 1))
		return ev, true, err
	default:
		return Event{}, false, internalf("unexpected revoke result")
	}
}

func (st *store) createDoc(ctx context.Context, doc docRecord, rev revRecord) (Event, error) {
	if doc.CreatedAt == "" {
		doc.CreatedAt, _ = stamp()
	}
	if doc.UpdatedAt == "" {
		doc.UpdatedAt = doc.CreatedAt
	}
	ts := doc.UpdatedAt
	score := scoreOf(ts)
	rev.CreatedAt = doc.CreatedAt
	docJSON, err := encodeJSON(doc)
	if err != nil {
		return Event{}, err
	}
	revJSON, err := encodeJSON(rev)
	if err != nil {
		return Event{}, err
	}
	evJSON, err := eventJSON(doc.SpaceID, ts, newEvent{
		Type: "document.created", ActorID: doc.CreatedBy, ResourceID: doc.ID, Visibility: "space", Revision: 1, Summary: doc.Title,
	})
	if err != nil {
		return Event{}, err
	}
	audit, err := encodeJSON(newAudit(doc.SpaceID, doc.CreatedBy, "document.create", "document", doc.ID, "ok", ts))
	if err != nil {
		return Event{}, err
	}
	arr, err := st.run(ctx, scriptCreateDoc, st.prefix, doc.SpaceID, doc.ID, docJSON, revJSON, evJSON, audit, ts, score)
	if err != nil {
		return Event{}, err
	}
	if strAt(arr, 0) != "ok" {
		return Event{}, internalf("unexpected document result")
	}
	return decodeEvent(strAt(arr, 1))
}

func (st *store) updateDoc(ctx context.Context, expected int, doc docRecord, rev revRecord) (Event, error) {
	if doc.UpdatedAt == "" {
		doc.UpdatedAt, _ = stamp()
	}
	ts := doc.UpdatedAt
	score := scoreOf(ts)
	rev.CreatedAt = ts
	docJSON, err := encodeJSON(doc)
	if err != nil {
		return Event{}, err
	}
	revJSON, err := encodeJSON(rev)
	if err != nil {
		return Event{}, err
	}
	evJSON, err := eventJSON(doc.SpaceID, ts, newEvent{
		Type: "document.updated", ActorID: doc.UpdatedBy, ResourceID: doc.ID, Visibility: "space", Revision: doc.Revision, Summary: doc.Title,
	})
	if err != nil {
		return Event{}, err
	}
	audit, err := encodeJSON(newAudit(doc.SpaceID, doc.UpdatedBy, "document.update", "document", doc.ID, "ok", ts))
	if err != nil {
		return Event{}, err
	}
	arr, err := st.run(ctx, scriptUpdateDoc, st.prefix, doc.SpaceID, doc.ID, strconv.Itoa(expected), docJSON, revJSON, strconv.Itoa(doc.Revision), evJSON, audit, ts, score)
	if err != nil {
		return Event{}, err
	}
	switch strAt(arr, 0) {
	case "missing":
		return Event{}, notFound("document not found")
	case "conflict":
		var current docRecord
		if err := json.Unmarshal([]byte(strAt(arr, 1)), &current); err != nil {
			return Event{}, err
		}
		api := current.api()
		return Event{}, conflict("document revision conflict", map[string]any{"current_revision": api.Revision, "document": api})
	case "ok":
		return decodeEvent(strAt(arr, 1))
	default:
		return Event{}, internalf("unexpected document result")
	}
}

func (st *store) deleteDoc(ctx context.Context, space, docID, actor string) (Event, error) {
	doc, err := st.getDoc(ctx, space, docID, false)
	if err != nil {
		return Event{}, err
	}
	ts, score := stamp()
	evJSON, err := eventJSON(space, ts, newEvent{
		Type: "document.deleted", ActorID: actor, ResourceID: docID, Visibility: "space", Revision: doc.Revision, Summary: doc.Title,
	})
	if err != nil {
		return Event{}, err
	}
	audit, err := encodeJSON(newAudit(space, actor, "document.delete", "document", docID, "ok", ts))
	if err != nil {
		return Event{}, err
	}
	arr, err := st.run(ctx, scriptDeleteDoc, st.prefix, space, docID, evJSON, audit, ts, score)
	if err != nil {
		return Event{}, err
	}
	switch strAt(arr, 0) {
	case "missing":
		return Event{}, notFound("document not found")
	case "ok":
		return decodeEvent(strAt(arr, 1))
	default:
		return Event{}, internalf("unexpected document result")
	}
}

func (st *store) createAsset(ctx context.Context, asset assetRecord) (Event, error) {
	if asset.CreatedAt == "" {
		asset.CreatedAt, _ = stamp()
	}
	ts := asset.CreatedAt
	score := scoreOf(ts)
	raw, err := encodeJSON(asset)
	if err != nil {
		return Event{}, err
	}
	evJSON, err := eventJSON(asset.SpaceID, ts, newEvent{
		Type: "asset.created", ActorID: asset.UploadedBy, ResourceID: asset.ID, Visibility: "space", Summary: asset.Filename,
	})
	if err != nil {
		return Event{}, err
	}
	audit, err := encodeJSON(newAudit(asset.SpaceID, asset.UploadedBy, "asset.upload", "asset", asset.ID, "ok", ts))
	if err != nil {
		return Event{}, err
	}
	arr, err := st.run(ctx, scriptCreateAsset, st.prefix, asset.SpaceID, asset.ID, raw, evJSON, audit, ts, score)
	if err != nil {
		return Event{}, err
	}
	if strAt(arr, 0) != "ok" {
		return Event{}, internalf("unexpected asset result")
	}
	ev, err := decodeEvent(strAt(arr, 1))
	if err != nil {
		return Event{}, err
	}
	return ev, nil
}

func (st *store) openDM(ctx context.Context, space, agentA, agentB, roomID string) (roomRecord, Event, bool, error) {
	ts, score := stamp()
	key := dmKey(agentA, agentB)
	room := roomRecord{ID: roomID, SpaceID: space, Type: "dm", DMKey: key, MemberIDs: []string{agentA, agentB}, CreatedAt: ts}
	if room.MemberIDs[0] > room.MemberIDs[1] {
		room.MemberIDs[0], room.MemberIDs[1] = room.MemberIDs[1], room.MemberIDs[0]
	}
	raw, err := encodeJSON(room)
	if err != nil {
		return roomRecord{}, Event{}, false, err
	}
	evJSON, err := eventJSON(space, ts, newEvent{
		Type: "room.created", ActorID: agentA, ResourceID: roomID, RoomID: roomID, Visibility: "room", Summary: "direct conversation",
	})
	if err != nil {
		return roomRecord{}, Event{}, false, err
	}
	arr, err := st.run(ctx, scriptOpenDM, st.prefix, space, key, raw, roomID, agentA, agentB, evJSON, ts, score)
	if err != nil {
		return roomRecord{}, Event{}, false, err
	}
	switch strAt(arr, 0) {
	case "exists":
		existing, err := st.getRoom(ctx, space, strAt(arr, 1))
		return existing, Event{}, false, err
	case "created":
		ev, err := decodeEvent(strAt(arr, 1))
		if err != nil {
			return roomRecord{}, Event{}, false, err
		}
		created, err := st.getRoom(ctx, space, roomID)
		return created, ev, true, err
	default:
		return roomRecord{}, Event{}, false, internalf("unexpected room result")
	}
}

func (st *store) sendMessage(ctx context.Context, space string, msg messageRecord, visibility string) (Event, string, error) {
	if msg.CreatedAt == "" {
		msg.CreatedAt, _ = stamp()
	}
	ts := msg.CreatedAt
	score := scoreOf(ts)
	if msg.Mentions == nil {
		msg.Mentions = []string{}
	}
	if msg.DocumentRefs == nil {
		msg.DocumentRefs = []DocRef{}
	}
	if msg.AssetIDs == nil {
		msg.AssetIDs = []string{}
	}
	raw, err := encodeJSON(msg)
	if err != nil {
		return Event{}, "", err
	}
	idemKey := ""
	if msg.IdempotencyKey != "" {
		idemKey = st.key("idem", msg.RoomID, msg.SenderID, msg.IdempotencyKey)
	}
	evJSON, err := eventJSON(space, ts, newEvent{
		Type: "message.created", ActorID: msg.SenderID, ResourceID: msg.ID, RoomID: msg.RoomID, Visibility: visibility, Summary: "message created",
	})
	if err != nil {
		return Event{}, "", err
	}
	audit, err := encodeJSON(newAudit(space, msg.SenderID, "message.create", "message", msg.ID, "ok", ts))
	if err != nil {
		return Event{}, "", err
	}
	arr, err := st.run(ctx, scriptSendMessage, st.prefix, msg.RoomID, raw, msg.ID, idemKey, msg.ReplyToMessageID, evJSON, audit, space, ts, score)
	if err != nil {
		return Event{}, "", err
	}
	switch strAt(arr, 0) {
	case "replay":
		return Event{}, strAt(arr, 1), nil
	case "badreply":
		return Event{}, "", badRequest("reply_to_message_id is not in this room")
	case "ok":
		ev, err := decodeEvent(strAt(arr, 1))
		return ev, "", err
	default:
		return Event{}, "", internalf("unexpected message result")
	}
}

func (st *store) ack(ctx context.Context, space, agent string, last int64) (string, error) {
	ts, score := stamp()
	audit, err := encodeJSON(newAudit(space, agent, "events.ack", "cursor", strconv.FormatInt(last, 10), "ok", ts))
	if err != nil {
		return "", err
	}
	arr, err := st.run(ctx, scriptAck, st.prefix, space, agent, strconv.FormatInt(last, 10), audit, score)
	if err != nil {
		return "", err
	}
	switch strAt(arr, 0) {
	case "nospace":
		return "", notFound("space not found")
	case "ahead":
		return "", badRequest("last_ack_seq is ahead of the server")
	case "ok":
		return ts, nil
	default:
		return "", internalf("unexpected ack result")
	}
}
