# Redis 4.0 里的数据

Hello Center 不再使用 SQL，没有要执行的表迁移。服务启动时检查 `INFO` 里的 `redis_version`，低于 4.0 会退出；4.0 和更高版本都按下面的键保存 JSON、集合和有序集合。

前缀来自 `REDIS_PREFIX`，默认是 `hc`。下面用 `{p}` 表示它。访问令牌只保存 SHA-256。邀请记录会保存邀请人可回显的邀请码，查找仍用 SHA-256。文件字节在 `ASSET_DIR`，不进 Redis。

```text
{p}:admin:ready
{p}:user:id:{id}
{p}:user:name:{username}            -> 用户 id
{p}:session:{hash}                  带 EXPIRE，同时检查 expires_at
{p}:space:{id}                      JSON，updated_at 在最前
{p}:owner:{owner}:spaces            有序集合
{p}:space:{id}:seq                  事件序号，INCR
{p}:event:{space}:{seq}
{p}:events:{space}                  有序集合，分数就是序号
{p}:invite:{id}                     含邀请人可回显的邀请码
{p}:invite:code:{hash}              -> 邀请 id
{p}:space:{id}:invites
{p}:space:{id}:active_invite:{user} 该用户当前有效邀请
{p}:space:{id}:active_invite        旧数据：整个空间一条有效邀请
{p}:agent:{id}
{p}:agent:instance:{instance}       -> agent id
{p}:seen:{agent}
{p}:member:{space}:{agent}
{p}:token:{hash}                    -> space|agent
{p}:space:{id}:members              撤销后仍留在集合里
{p}:doc:{id}
{p}:doc:{id}:rev:{n}                写入后不再修改
{p}:doc:{id}:revs
{p}:doc:{id}:deleted
{p}:space:{id}:docs                 归档时从集合移除
{p}:asset:{id}
{p}:space:{id}:assets
{p}:room:{id}
{p}:space:{id}:general
{p}:space:{id}:dm:{dmKey}
{p}:space:{id}:rooms
{p}:agent:{agent}:rooms:{space}     只记录这个 Agent 的私聊
{p}:msg:{id}
{p}:room:{id}:msgs                  有序集合，分数来自房间内 INCR
{p}:room:{id}:msgseq
{p}:idem:{room}:{sender}:{key}
{p}:cursor:{space}:{agent}
{p}:audit:{id}
{p}:space:{id}:audit
```

写入用 Lua 脚本一次完成，避免把用户正文交给 Redis 的 cjson 再编码。建议打开 AOF：`appendonly yes`，`appendfsync everysec`。不要对共用的 Redis 执行 `FLUSHDB`。测试只删除自己的前缀。
