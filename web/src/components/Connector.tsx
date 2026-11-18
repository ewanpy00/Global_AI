import { useState } from "react";
import { connectorChat, LABEL_TEXT, type AppConfig, type ChatTurn, type Report } from "../api";
import { Markdown } from "./Markdown";

interface Props {
  config: AppConfig;
  onInspect: (text: string) => void;
}

interface Msg extends ChatTurn {
  report?: Report | null;
  error?: string;
  model?: string;
}

const MODES = [
  { id: "annotate", label: "Annotate", hint: "append a freshness footnote to the answer" },
  { id: "correct", label: "Correct", hint: "rewrite outdated claims in place and list the changes" },
  { id: "report", label: "Report", hint: "leave the text; return the report as JSON / headers" },
  { id: "off", label: "Off", hint: "plain pass-through via Bifrost" },
];

export function Connector({ config, onInspect }: Props) {
  const [mode, setMode] = useState(config.connector.default_mode || "annotate");
  const [model, setModel] = useState("freshgate");
  const [msgs, setMsgs] = useState<Msg[]>([]);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const base = `${window.location.origin}/v1`;

  async function send() {
    const content = input.trim();
    if (!content || busy) return;
    const history: Msg[] = [...msgs, { role: "user", content }];
    setMsgs(history);
    setInput("");
    setBusy(true);
    try {
      // Send the conversation as the model saw it: the original answers, without footnotes.
      const turns: ChatTurn[] = history.map((m) => ({ role: m.role, content: m.report?.text ?? m.content }));
      const r = await connectorChat(turns, mode, model);
      setMsgs([...history, { role: "assistant", content: r.content, report: r.report, error: r.error, model: r.model }]);
    } catch (e) {
      setMsgs([...history, { role: "assistant", content: "", error: e instanceof Error ? e.message : String(e) }]);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="connector">
      <section className="panel connector-info">
        <h2>Use it as a connector</h2>
        <p>
          Freshgate exposes an <b>OpenAI-compatible endpoint</b>. Point any app, agent framework or chat UI at it instead of the model
          provider. Requests go through an embedded <b>Bifrost</b> gateway, and a Bifrost plugin checks every answer against live{" "}
          <b>Tavily</b> results before it is returned.
        </p>
        <div className="flow">
          <span className="flow-node">Your app</span>
          <span className="flow-arrow">→</span>
          <span className="flow-node flow-bifrost">Bifrost gateway</span>
          <span className="flow-arrow">→</span>
          <span className="flow-node">LLM provider</span>
          <span className="flow-arrow">→</span>
          <span className="flow-node flow-plugin">Freshness plugin (PostLLMHook)</span>
          <span className="flow-arrow">→</span>
          <span className="flow-node">checked answer</span>
        </div>

        <h4>Base URL</h4>
        <pre className="code">{base}</pre>

        <h4>Python (OpenAI SDK)</h4>
        <pre className="code">{`from openai import OpenAI

client = OpenAI(base_url="${base}", api_key="unused")
reply = client.chat.completions.create(
    model="freshgate",  # or "anthropic/claude-haiku-5-5", "openai/gpt-5-mini", ...
    messages=[{"role": "user", "content": "What does GitHub Copilot cost?"}],
    extra_headers={"X-Freshgate-Mode": "annotate"},
)
print(reply.choices[0].message.content)`}</pre>

        <h4>curl</h4>
        <pre className="code">{`curl ${base}/chat/completions \\
  -H 'Content-Type: application/json' \\
  -H 'X-Freshgate-Mode: correct' \\
  -d '{"model":"freshgate","messages":[{"role":"user","content":"Latest stable Node.js LTS?"}]}'`}</pre>

        <h4>Modes (header X-Freshgate-Mode)</h4>
        <ul className="modes">
          {MODES.map((m) => (
            <li key={m.id}>
              <code>{m.id}</code>: {m.hint}
            </li>
          ))}
        </ul>
        <p className="muted small">
          The full report is returned in the <code>freshgate</code> field of the response and as <code>X-Freshgate-Score</code> /{" "}
          <code>X-Freshgate-Label</code> headers.
        </p>

        <h2 className="mcp-title">Use it from an agent (MCP)</h2>
        <p>
          Freshgate is also a <b>Model Context Protocol</b> server. Agents in Claude Code, Claude Desktop, Cursor and others get four tools:{" "}
          <code>check_freshness</code>, <code>correct_text</code>, <code>audit_site</code> and <code>compare_models</code>, so they can check their
          own answer before sending it.
        </p>
        <h4>Claude Code (HTTP)</h4>
        <pre className="code">{`claude mcp add --transport http freshgate ${window.location.origin}/mcp`}</pre>
        <h4>Claude Desktop / Cursor (stdio)</h4>
        <pre className="code">{`{
  "mcpServers": {
    "freshgate": {
      "command": "/path/to/Global_AI/backend/bin/freshgate",
      "args": ["-mcp"],
      "env": { "TAVILY_API_KEY": "…", "GROQ_API_KEY": "…" }
    }
  }
}`}</pre>
      </section>

      <section className="panel playground">
        <div className="panel-head">
          <h2>Playground</h2>
          <span className="spacer" />
          <input className="model-input mono" value={model} onChange={(e) => setModel(e.target.value)} title="model (provider/model or freshgate)" />
        </div>
        <div className="segmented">
          {MODES.map((m) => (
            <button key={m.id} className={mode === m.id ? "is-on" : ""} onClick={() => setMode(m.id)} title={m.hint}>
              {m.label}
            </button>
          ))}
        </div>

        {!config.connector.available && (
          <div className="banner">
            The connector needs live mode. Set <code>TAVILY_API_KEY</code> and a provider key such as <code>ANTHROPIC_API_KEY</code>, then
            restart the backend. The Inspector tab works in demo mode.
          </div>
        )}

        <div className="chat">
          {msgs.length === 0 && <p className="muted small pad">Ask something time-sensitive: prices, latest versions, who runs a company…</p>}
          {msgs.map((m, i) => (
            <div key={i} className={`msg msg-${m.role}`}>
              {m.role === "assistant" && m.report && (
                <div className={`msg-badge label-bg-${m.report.summary.label}`}>
                  {m.report.summary.score}/100 · {LABEL_TEXT[m.report.summary.label]} · {m.report.summary.total} claims
                  <button className="link" onClick={() => onInspect(m.report!.text)}>
                    Inspect →
                  </button>
                </div>
              )}
              {m.content && <Markdown text={m.content} />}
              {m.error && <div className="small err">{m.error}</div>}
            </div>
          ))}
          {busy && (
            <div className="msg msg-assistant">
              <span className="pulse-dot" /> Model answering, then Freshgate verifying the answer…
            </div>
          )}
        </div>
        <form
          className="chat-input"
          onSubmit={(e) => {
            e.preventDefault();
            void send();
          }}
        >
          <input value={input} onChange={(e) => setInput(e.target.value)} placeholder="Message…" disabled={!config.connector.available} />
          <button className="btn btn-primary" disabled={busy || !input.trim() || !config.connector.available}>
            Send
          </button>
        </form>
      </section>
    </div>
  );
}
