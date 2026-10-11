import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeHighlight from "rehype-highlight";

/**
 * Markdown renders agent output the way the agents write it — paragraphs, lists,
 * tables, inline code — with fenced code blocks syntax-highlighted. Raw HTML is
 * not enabled, so agent output cannot inject markup.
 *
 * Tables are wrapped in a scroll container: a wide table scrolls instead of
 * crushing its columns.
 */
export function Markdown({ text }: { text: string }) {
  return (
    <div className="md">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[[rehypeHighlight, { detect: true, ignoreMissing: true }]]}
        components={{
          table(props) {
            return (
              <div className="md-table-wrap">
                <table>{props.children}</table>
              </div>
            );
          },
        }}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
}
