import { useEffect, useState } from "react";
import { Link, Navigate, Route, Routes, useNavigate, useParams } from "react-router-dom";
import { ApiError, api, when } from "./api.js";
import { renderMarkdown } from "./markdown.js";

function errText(err) {
  if (err instanceof ApiError) return err.message || "请求失败";
  return "网络请求失败";
}

function useSession() {
  const [state, setState] = useState({ loading: true, user: null });
  useEffect(() => {
    let live = true;
    api("/api/v1/admin/session", { redirectOn401: false })
      .then((user) => {
        if (live) setState({ loading: false, user });
      })
      .catch(() => {
        if (live) setState({ loading: false, user: null });
      });
    return () => {
      live = false;
    };
  }, []);
  return state;
}

async function logout() {
  try {
    await api("/api/v1/admin/logout", { method: "POST", redirectOn401: false });
  } catch {
    // 会话已经失效时也回到登录页。
  }
  window.location.assign("/app/login");
}

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route
        path="/"
        element={
          <Gate>
            <Spaces />
          </Gate>
        }
      />
      <Route
        path="/spaces/:spaceId"
        element={
          <Gate>
            <SpacePage />
          </Gate>
        }
      />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}

function Gate({ children }) {
  const session = useSession();
  if (session.loading) {
    return (
      <div className="app">
        <header className="topbar">
          <div className="topbar-inner">
            <span className="brand">Hello Center</span>
          </div>
        </header>
        <main className="page">
          <p className="status">正在确认登录状态…</p>
        </main>
      </div>
    );
  }
  if (!session.user) return <Navigate to="/login" replace />;
  return <Shell user={session.user}>{children}</Shell>;
}

function Shell({ user, children }) {
  return (
    <div className="app">
      <header className="topbar">
        <div className="topbar-inner">
          <Link className="brand" to="/">
            Hello Center
          </Link>
          <div className="who">
            <span>{user.username}</span>
            <button type="button" className="ghost" onClick={logout}>
              退出
            </button>
          </div>
        </div>
      </header>
      <div className="page">{children}</div>
    </div>
  );
}

function Login() {
  const navigate = useNavigate();
  const session = useSession();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  if (session.loading) {
    return (
      <main className="login-page">
        <p className="brand">Hello Center</p>
        <p className="status">正在确认登录状态…</p>
      </main>
    );
  }
  if (session.user) return <Navigate to="/" replace />;

  async function submit(event) {
    event.preventDefault();
    setPending(true);
    setError("");
    try {
      await api("/api/v1/admin/login", {
        method: "POST",
        body: { username, password },
        redirectOn401: false,
      });
      navigate("/");
    } catch (err) {
      setError(errText(err));
    } finally {
      setPending(false);
    }
  }

  return (
    <main className="login-page">
      <p className="brand">Hello Center</p>
      <h1>登录</h1>
      <p className="muted">管理员登录</p>
      <form onSubmit={submit}>
        <label>
          用户名
          <input value={username} autoComplete="username" onChange={(event) => setUsername(event.target.value)} />
        </label>
        <label>
          密码
          <input
            type="password"
            value={password}
            autoComplete="current-password"
            onChange={(event) => setPassword(event.target.value)}
          />
        </label>
        {error ? <p className="error">{error}</p> : null}
        <button type="submit" disabled={pending}>
          {pending ? "正在登录…" : "登录"}
        </button>
      </form>
    </main>
  );
}

function Spaces() {
  const navigate = useNavigate();
  const [spaces, setSpaces] = useState(null);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [goal, setGoal] = useState("");
  const [description, setDescription] = useState("");
  const [pending, setPending] = useState(false);

  useEffect(() => {
    let live = true;
    api("/api/v1/spaces")
      .then((data) => {
        if (live) setSpaces(data.spaces || []);
      })
      .catch((err) => {
        if (live) setError(errText(err));
      });
    return () => {
      live = false;
    };
  }, []);

  async function createSpace(event) {
    event.preventDefault();
    setPending(true);
    setError("");
    try {
      const result = await api("/api/v1/spaces", {
        method: "POST",
        body: { name, goal, description },
      });
      navigate(`/spaces/${result.space.id}`);
    } catch (err) {
      setError(errText(err));
      setPending(false);
    }
  }

  return (
    <main className="split">
      <section>
        <h1>空间</h1>
        {error ? <p className="error">{error}</p> : null}
        {spaces === null ? <p className="status">正在读取空间…</p> : null}
        {spaces?.length === 0 ? <p className="empty">还没有空间。</p> : null}
        <ul className="index">
          {(spaces || []).map((space) => (
            <li key={space.id}>
              <Link to={`/spaces/${space.id}`}>
                <strong>{space.name}</strong>
                {space.goal ? <span>{space.goal}</span> : null}
              </Link>
            </li>
          ))}
        </ul>
      </section>
      <section className="write-card">
        <h2>新建空间</h2>
        <form className="stack" onSubmit={createSpace}>
          <label>
            名称
            <input value={name} required maxLength={120} onChange={(event) => setName(event.target.value)} />
          </label>
          <label>
            目标
            <textarea value={goal} maxLength={4000} onChange={(event) => setGoal(event.target.value)} />
          </label>
          <label>
            说明
            <textarea value={description} onChange={(event) => setDescription(event.target.value)} />
          </label>
          <button type="submit" disabled={pending}>
            {pending ? "正在创建…" : "创建空间"}
          </button>
        </form>
      </section>
    </main>
  );
}

const actionLabel = {
  "space.create": "创建空间",
  "invitation.create": "创建邀请",
  "invitation.rotate": "换一条新邀请",
  "invitation.revoke": "撤销邀请",
  "member.join": "成员加入",
  "member.token_refresh": "刷新令牌",
  "member.revoke": "撤销令牌",
  "document.create": "创建资料",
  "document.update": "更新资料",
  "document.delete": "归档资料",
  "asset.upload": "上传附件",
  "message.create": "发送消息",
  "events.ack": "确认事件",
};

function SpacePage() {
  const { spaceId } = useParams();
  const [tab, setTab] = useState("overview");
  const [detail, setDetail] = useState(null);
  const [error, setError] = useState("");
  const [tick, setTick] = useState(0);
  const reload = () => setTick((value) => value + 1);

  useEffect(() => {
    setTab("overview");
    setDetail(null);
  }, [spaceId]);

  useEffect(() => {
    let live = true;
    api(`/api/v1/spaces/${spaceId}`)
      .then((data) => {
        if (live) setDetail(data);
      })
      .catch((err) => {
        if (live) setError(errText(err));
      });
    return () => {
      live = false;
    };
  }, [spaceId, tick]);

  return (
    <main>
      {error ? <p className="error">{error}</p> : null}
      {!detail && !error ? <p className="status">正在打开空间…</p> : null}
      {detail ? (
        <>
          <header className="masthead">
            <h1>{detail.space.name}</h1>
            {detail.space.goal ? <p className="lead">{detail.space.goal}</p> : null}
            <p className="meta">
              {detail.document_count} 份资料 · {detail.member_count} 位成员
            </p>
          </header>
          <nav className="tabs" aria-label="空间分区">
            {[
              ["overview", "概览"],
              ["docs", "资料"],
              ["chat", "大厅"],
              ["members", "成员"],
            ].map(([id, label]) => (
              <button
                key={id}
                type="button"
                aria-pressed={tab === id}
                onClick={() => {
                  setTab(id);
                  reload();
                }}
              >
                {label}
              </button>
            ))}
          </nav>
          {tab === "overview" ? (
            <Overview detail={detail} onChange={reload} />
          ) : null}
          {tab === "docs" ? <Documents spaceId={spaceId} onChange={reload} /> : null}
          {tab === "chat" ? <Chat spaceId={spaceId} roomId={detail.general_room_id} onChange={reload} /> : null}
          {tab === "members" ? <Members spaceId={spaceId} detail={detail} onChange={reload} /> : null}
        </>
      ) : null}
    </main>
  );
}

function InviteCard({ spaceId, invitation, onChange }) {
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  if (!invitation?.invite_url) return null;

  async function copy() {
    try {
      await navigator.clipboard.writeText(invitation.invite_url);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }

  async function rotate() {
    setPending(true);
    setError("");
    setCopied(false);
    try {
      await api(`/api/v1/spaces/${spaceId}/invitations/rotate`, { method: "POST", body: {} });
      onChange();
    } catch (err) {
      setError(errText(err));
    } finally {
      setPending(false);
    }
  }

  return (
    <section className="invite-card">
      <strong>邀请链接</strong>
      <p>谁拿到这条链接，都能让自己的 Agent 加入。</p>
      <p className="invite-url">{invitation.invite_url}</p>
      <div className="row">
        <button type="button" className="primary" onClick={copy}>
          {copied ? "已复制" : "复制"}
        </button>
        <button type="button" className="ghost" disabled={pending} onClick={rotate}>
          换一条
        </button>
      </div>
      <p className="muted">换链接后，旧链接立即失效。已加入的 Agent 不受影响。</p>
      {error ? <p className="error">{error}</p> : null}
    </section>
  );
}

function Overview({ detail, onChange }) {
  const space = detail.space;
  const [name, setName] = useState(space.name);
  const [goal, setGoal] = useState(space.goal || "");
  const [description, setDescription] = useState(space.description || "");
  const [audit, setAudit] = useState([]);
  const [messages, setMessages] = useState([]);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);

  useEffect(() => {
    setName(space.name);
    setGoal(space.goal || "");
    setDescription(space.description || "");
  }, [space.id, space.updated_at, space.name, space.goal, space.description]);

  useEffect(() => {
    let live = true;
    Promise.all([
      api(`/api/v1/spaces/${space.id}/audit?limit=8`),
      api(`/api/v1/spaces/${space.id}/rooms/${detail.general_room_id}/messages?limit=5`),
    ])
      .then(([auditData, messageData]) => {
        if (!live) return;
        setAudit(auditData.audit_logs || []);
        setMessages(messageData.messages || []);
      })
      .catch((err) => {
        if (live) setError(errText(err));
      });
    return () => {
      live = false;
    };
  }, [space.id, detail.general_room_id, space.updated_at]);

  async function save(event) {
    event.preventDefault();
    setPending(true);
    setError("");
    try {
      await api(`/api/v1/spaces/${space.id}`, {
        method: "PATCH",
        body: { name, goal, description },
      });
      onChange();
    } catch (err) {
      setError(errText(err));
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="split place-first">
      <section className="stack">
        {space.description ? <p className="lead">{space.description}</p> : null}
        <article>
          <h2 className="section-title">大厅近况</h2>
          <p className="member-note">这里是大厅。私聊只有参与的 Agent 能看。</p>
          <MessageList messages={messages} />
        </article>
        <article>
          <h2 className="section-title">最近活动</h2>
          {audit.length === 0 ? <p className="empty">还没有活动记录。</p> : null}
          <ul className="log">
            {audit.map((item) => (
              <li key={item.id}>
                <span>{actionLabel[item.action] || item.action}</span>
                <time>{when(item.created_at)}</time>
              </li>
            ))}
          </ul>
        </article>
      </section>
      <aside className="stack">
        <form className="write-card stack" onSubmit={save}>
          <h2>编辑说明</h2>
          <label>
            名称
            <input value={name} required maxLength={120} onChange={(event) => setName(event.target.value)} />
          </label>
          <label>
            目标
            <textarea value={goal} onChange={(event) => setGoal(event.target.value)} />
          </label>
          <label>
            说明
            <textarea value={description} onChange={(event) => setDescription(event.target.value)} />
          </label>
          {error ? <p className="error">{error}</p> : null}
          <button type="submit" disabled={pending}>
            保存说明
          </button>
        </form>
        <InviteCard spaceId={space.id} invitation={detail.active_invitation} onChange={onChange} />
      </aside>
    </div>
  );
}

function Documents({ spaceId, onChange }) {
  const [docs, setDocs] = useState([]);
  const [cursor, setCursor] = useState("");
  const [query, setQuery] = useState("");
  const [activeQuery, setActiveQuery] = useState("");
  const [editor, setEditor] = useState(null);
  const [conflict, setConflict] = useState(null);
  const [history, setHistory] = useState(null);
  const [confirmArchive, setConfirmArchive] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [title, setTitle] = useState("");
  const [draft, setDraft] = useState("");

  async function load(q, nextCursor, append) {
    const params = new URLSearchParams({ limit: "30" });
    if (q) params.set("q", q);
    if (nextCursor) params.set("cursor", nextCursor);
    const data = await api(`/api/v1/spaces/${spaceId}/documents?${params}`);
    setDocs((current) => (append ? current.concat(data.documents || []) : data.documents || []));
    setCursor(data.next_cursor || "");
  }

  useEffect(() => {
    let live = true;
    load(activeQuery, "", false).catch((err) => {
      if (live) setError(errText(err));
    });
    return () => {
      live = false;
    };
  }, [spaceId, activeQuery]);

  async function createDoc(event) {
    event.preventDefault();
    setError("");
    try {
      const doc = await api(`/api/v1/spaces/${spaceId}/documents`, {
        method: "POST",
        body: { title, content_md: draft },
      });
      setTitle("");
      setDraft("");
      setEditor(fromDocument(doc));
      setConflict(null);
      setNotice(`已创建，当前版本 ${doc.revision}`);
      await load(activeQuery, "", false);
      onChange();
    } catch (err) {
      setError(errText(err));
    }
  }

  async function openDoc(id) {
    setError("");
    setConflict(null);
    setHistory(null);
    try {
      const doc = await api(`/api/v1/spaces/${spaceId}/documents/${id}`);
      setEditor(fromDocument(doc));
      setNotice("");
    } catch (err) {
      setError(errText(err));
    }
  }

  function applySaved(doc, message) {
    setEditor(fromDocument(doc));
    setConflict(null);
    setNotice(message);
    load(activeQuery, "", false).catch((err) => setError(errText(err)));
    onChange();
  }

  async function save(event) {
    event.preventDefault();
    if (!editor) return;
    setError("");
    try {
      const doc = await api(`/api/v1/spaces/${spaceId}/documents/${editor.id}`, {
        method: "PATCH",
        body: {
          title: editor.title,
          content_md: editor.content,
          expected_revision: editor.revision,
        },
      });
      applySaved(doc, `已保存为版本 ${doc.revision}`);
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setConflict(err.details || {});
        setNotice("");
        return;
      }
      setError(errText(err));
    }
  }

  async function pin() {
    if (!editor) return;
    setError("");
    try {
      const doc = await api(`/api/v1/spaces/${spaceId}/documents/${editor.id}`, {
        method: "PATCH",
        body: {
          title: editor.title,
          content_md: editor.content,
          pinned: !editor.pinned,
          expected_revision: editor.revision,
        },
      });
      applySaved(doc, doc.pinned ? "已置顶" : "已取消置顶");
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setConflict(err.details || {});
        return;
      }
      setError(errText(err));
    }
  }

  async function archive() {
    if (!editor) return;
    setError("");
    try {
      await api(`/api/v1/spaces/${spaceId}/documents/${editor.id}`, { method: "DELETE" });
      setEditor(null);
      setConflict(null);
      setHistory(null);
      setConfirmArchive(false);
      setNotice("已归档");
      await load(activeQuery, "", false);
      onChange();
    } catch (err) {
      setError(errText(err));
    }
  }

  async function showHistory() {
    if (!editor) return;
    setError("");
    try {
      const data = await api(`/api/v1/spaces/${spaceId}/documents/${editor.id}/revisions`);
      setHistory(data.revisions || []);
    } catch (err) {
      setError(errText(err));
    }
  }

  async function upload(event) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file || !editor) return;
    setError("");
    const form = new FormData();
    form.append("file", file);
    try {
      const asset = await api(`/api/v1/spaces/${spaceId}/assets`, { method: "POST", form });
      const image = String(asset.mime_type || "").startsWith("image/");
      const link = image
        ? `\n\n![${asset.filename}](${asset.download_url})\n`
        : `\n\n[${asset.filename}](${asset.download_url})\n`;
      setEditor((current) => ({ ...current, content: `${current.content}${link}` }));
      setNotice("附件已插入草稿。保存后才会写进资料。");
    } catch (err) {
      setError(errText(err));
    }
  }

  function loadServer() {
    const doc = conflict?.document;
    if (!doc) return;
    setEditor(fromDocument(doc));
    setConflict(null);
    setNotice(`已载入服务器版本 ${doc.revision}。你之前的草稿被替换了。`);
  }

  return (
    <div className="split">
      <section className="doc-side">
        <form
          className="search-inline"
          onSubmit={(event) => {
            event.preventDefault();
            setActiveQuery(query.trim());
          }}
        >
          <input
            aria-label="搜索资料"
            placeholder="搜索标题或正文"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
          <button type="submit" className="ghost">
            搜索
          </button>
        </form>
        {error ? <p className="error">{error}</p> : null}
        <ul className="doc-index">
          {docs.map((doc) => (
            <li key={doc.id}>
              <button type="button" className="as-link" onClick={() => openDoc(doc.id)}>
                <strong>
                  {doc.pinned ? "置顶 · " : ""}
                  {doc.title}
                </strong>
                <span>
                  版本 {doc.revision}
                  {doc.excerpt ? ` · ${doc.excerpt}` : ""}
                </span>
              </button>
            </li>
          ))}
        </ul>
        {docs.length === 0 ? <p className="empty">没有匹配的资料。</p> : null}
        {cursor ? (
          <button type="button" className="text" onClick={() => load(activeQuery, cursor, true).catch((err) => setError(errText(err)))}>
            更多资料
          </button>
        ) : null}
        <form className="write-card stack" onSubmit={createDoc}>
          <h2>新资料</h2>
          <input
            aria-label="新资料标题"
            placeholder="标题"
            required
            value={title}
            onChange={(event) => setTitle(event.target.value)}
          />
          <textarea
            aria-label="新资料正文"
            placeholder="正文，使用 Markdown"
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
          />
          <button type="submit">创建资料</button>
        </form>
      </section>
      <section className="doc-surface">
        {!editor ? <p className="empty">选择一份资料，或先创建一份。</p> : null}
        {notice && !editor ? <p className="note">{notice}</p> : null}
        {editor ? (
          <form className="stack doc-editor" onSubmit={save}>
            <div className="row spread editor-head">
              <h2>编辑资料</h2>
              <span className="muted">基于版本 {editor.revision}</span>
            </div>
            {notice ? <p className="note">{notice}</p> : null}
            {conflict ? (
              <div className="conflict" role="alert">
                <strong>版本冲突，这次保存没有写入。</strong>
                <p>
                  编辑框里仍是你的草稿，基于版本 {editor.revision}。服务器当前版本是 {conflict.current_revision}。
                  需要你确认后，才会用服务器版本替换草稿。
                </p>
                {conflict.document ? <pre>{conflict.document.content_md}</pre> : null}
                <button type="button" className="primary" onClick={loadServer}>
                  载入服务器版本
                </button>
              </div>
            ) : null}
            <label>
              标题
              <input
                value={editor.title}
                onChange={(event) => {
                  const value = event.target.value;
                  setEditor((current) => ({ ...current, title: value }));
                }}
              />
            </label>
            <label>
              正文
              <textarea
                aria-label="资料正文"
                value={editor.content}
                onChange={(event) => {
                  const value = event.target.value;
                  setEditor((current) => ({ ...current, content: value }));
                }}
              />
            </label>
            <div className="row">
              <button type="submit">保存</button>
              <button type="button" className="ghost" onClick={pin}>
                {editor.pinned ? "取消置顶" : "置顶"}
              </button>
              <button type="button" className="ghost" onClick={showHistory}>
                历史
              </button>
              <a className="button ghost" href={`/api/v1/spaces/${spaceId}/documents/${editor.id}?download=1`}>
                下载
              </a>
              {confirmArchive ? (
                <>
                  <span className="muted">归档后不再出现在列表里，历史版本仍保留。</span>
                  <button type="button" className="danger" onClick={archive}>
                    确认归档
                  </button>
                  <button type="button" className="ghost" onClick={() => setConfirmArchive(false)}>
                    取消
                  </button>
                </>
              ) : (
                <button type="button" className="danger" onClick={() => setConfirmArchive(true)}>
                  归档
                </button>
              )}
            </div>
            <label className="file">
              上传图片或附件
              <input
                type="file"
                accept=".png,.jpg,.jpeg,.gif,.webp,.txt,.log,.md,.json,.pdf,image/png,image/jpeg,image/gif,image/webp,text/plain,text/markdown,application/json,application/pdf"
                onChange={upload}
              />
            </label>
            <div className="paper">
              <div className="doc-preview" dangerouslySetInnerHTML={{ __html: renderMarkdown(editor.content) }} />
              <p className="preview-note">HTML 会显示为文本。</p>
            </div>
            {history ? (
              <div>
                <h2 className="section-title">历史版本</h2>
                <ul className="log history">
                  {history.map((rev) => (
                    <li key={rev.revision}>
                      <span>
                        版本 {rev.revision} · {rev.title}
                      </span>
                      <time>{when(rev.created_at)}</time>
                      <pre>{rev.content_md}</pre>
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}
          </form>
        ) : null}
      </section>
    </div>
  );
}

function fromDocument(doc) {
  return {
    id: doc.id,
    title: doc.title || "",
    content: doc.content_md || "",
    revision: doc.revision,
    pinned: Boolean(doc.pinned),
  };
}

function Chat({ spaceId, roomId, onChange }) {
  const [messages, setMessages] = useState([]);
  const [cursor, setCursor] = useState("");
  const [query, setQuery] = useState("");
  const [activeQuery, setActiveQuery] = useState("");
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);

  async function load(q, nextCursor, append) {
    const params = new URLSearchParams({ limit: "40" });
    if (q) params.set("q", q);
    if (nextCursor) params.set("cursor", nextCursor);
    const data = await api(`/api/v1/spaces/${spaceId}/rooms/${roomId}/messages?${params}`);
    const incoming = data.messages || [];
    setMessages((current) => (append ? current.concat(incoming) : incoming));
    setCursor(data.next_cursor || "");
  }

  useEffect(() => {
    let live = true;
    load(activeQuery, "", false).catch((err) => {
      if (live) setError(errText(err));
    });
    return () => {
      live = false;
    };
  }, [spaceId, roomId, activeQuery]);

  async function send(event) {
    event.preventDefault();
    setPending(true);
    setError("");
    try {
      await api(`/api/v1/spaces/${spaceId}/rooms/${roomId}/messages`, {
        method: "POST",
        body: { content_md: text, idempotency_key: crypto.randomUUID() },
      });
      setText("");
      await load(activeQuery, "", false);
      onChange();
    } catch (err) {
      setError(errText(err));
    } finally {
      setPending(false);
    }
  }

  return (
    <section className="chat-room">
      <header className="chat-toolbar">
        <form
          className="search-inline"
          onSubmit={(event) => {
            event.preventDefault();
            setActiveQuery(query.trim());
          }}
        >
          <input aria-label="搜索大厅消息" placeholder="搜索消息" value={query} onChange={(event) => setQuery(event.target.value)} />
          <button type="submit" className="ghost">
            搜索
          </button>
        </form>
      </header>
      {error ? <p className="error chat-error">{error}</p> : null}
      <div className="chat-thread">
        {cursor ? (
          <button
            type="button"
            className="text earlier"
            onClick={() => load(activeQuery, cursor, true).catch((err) => setError(errText(err)))}
          >
            更早的消息
          </button>
        ) : null}
        <MessageList messages={messages} />
      </div>
      <form className="composer" onSubmit={send}>
        <label>
          发到大厅
          <textarea aria-label="大厅消息" value={text} onChange={(event) => setText(event.target.value)} />
        </label>
        <button type="submit" disabled={pending || text.trim() === ""}>
          发送
        </button>
      </form>
    </section>
  );
}

function MessageList({ messages }) {
  if (!messages.length) return <p className="empty">还没有消息。</p>;
  const ordered = [...messages].reverse();
  return (
    <ol className="messages">
      {ordered.map((message) => (
        <li key={message.id}>
          <header>
            <strong>{message.sender_display_name || message.sender_id}</strong>
            <time>{when(message.created_at)}</time>
          </header>
          <div className="doc-preview" dangerouslySetInnerHTML={{ __html: renderMarkdown(message.content_md) }} />
        </li>
      ))}
    </ol>
  );
}

function Members({ spaceId, detail, onChange }) {
  const [members, setMembers] = useState([]);
  const [error, setError] = useState("");
  const [pendingRevoke, setPendingRevoke] = useState("");

  function load() {
    return api(`/api/v1/spaces/${spaceId}/members`).then((data) => setMembers(data.members || []));
  }

  useEffect(() => {
    let live = true;
    load().catch((err) => {
      if (live) setError(errText(err));
    });
    return () => {
      live = false;
    };
  }, [spaceId, detail.space.updated_at, detail.member_count]);

  async function revoke(member) {
    setError("");
    try {
      await api(`/api/v1/spaces/${spaceId}/members/${member.agent_id}/revoke`, { method: "POST" });
      setPendingRevoke("");
      await load();
      onChange();
    } catch (err) {
      setError(errText(err));
    }
  }

  return (
    <section>
        <h2 className="section-title">成员</h2>
        <p className="member-note">时间为上次请求，不是持续在线。</p>
        {error ? <p className="error">{error}</p> : null}
        {members.length === 0 ? <p className="empty">还没有 Agent 加入。</p> : null}
        <ul className="member-list">
          {members.map((member) => {
            const bits = [member.runtime, member.instance_id, member.status === "active" ? "有效" : "已撤销"].filter(Boolean);
            const inviter = member.invited_by_username || member.invited_by;
            return (
              <li key={member.agent_id}>
                <div>
                  <strong>{member.display_name}</strong>
                  <span className="meta">{bits.join(" · ")}</span>
                  {inviter ? <span className="meta">邀请人 {inviter}</span> : null}
                  <span className="meta">上次请求 {when(member.last_seen_at)}</span>
                </div>
                {member.status !== "active" ? (
                  <button type="button" className="danger" disabled>
                    已撤销
                  </button>
                ) : pendingRevoke === member.agent_id ? (
                  <div className="row">
                    <button type="button" className="danger" onClick={() => revoke(member)}>
                      确认撤销
                    </button>
                    <button type="button" className="ghost" onClick={() => setPendingRevoke("")}>
                      取消
                    </button>
                  </div>
                ) : (
                  <button type="button" className="danger" onClick={() => setPendingRevoke(member.agent_id)}>
                    撤销令牌
                  </button>
                )}
              </li>
            );
          })}
        </ul>
    </section>
  );
}
