export class ApiError extends Error {
  constructor(status, body) {
    super(body?.error?.message || "请求失败");
    this.status = status;
    this.code = body?.error?.code || "";
    this.details = body?.error?.details;
  }
}

export async function api(path, options = {}) {
  const { method = "GET", body, form, redirectOn401 = true } = options;
  const headers = { "X-Hello-Center": "1" };
  let payload;
  if (form) {
    payload = form;
  } else if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }
  const response = await fetch(path, {
    method,
    headers,
    body: payload,
    credentials: "include",
  });
  const text = await response.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { raw: text };
    }
  }
  if (!response.ok) {
    if (response.status === 401 && redirectOn401 && !path.endsWith("/admin/login")) {
      window.location.assign("/app/login");
    }
    throw new ApiError(response.status, data);
  }
  return data;
}

const timeFormat = new Intl.DateTimeFormat("zh-CN", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});

export function when(value) {
  if (!value) return "还没有记录";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "还没有记录";
  return timeFormat.format(date);
}
