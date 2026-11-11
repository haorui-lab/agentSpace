import MarkdownIt from "markdown-it";

const markdown = new MarkdownIt({ html: false, linkify: false, breaks: true });

markdown.validateLink = (url) => {
  const value = String(url || "").trim();
  if (value.startsWith("/api/v1/spaces/") && !value.includes("..") && !value.includes("\\")) {
    return true;
  }
  return /^https?:\/\//i.test(value);
};

export function renderMarkdown(source) {
  return markdown.render(source || "");
}
