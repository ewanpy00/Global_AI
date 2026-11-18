import { useEffect, useState } from "react";
import { fetchConfig, type AppConfig } from "./api";
import { Arena } from "./components/Arena";
import { Audit } from "./components/Audit";
import { Connector } from "./components/Connector";
import { Documents } from "./components/Documents";
import { Inspector } from "./components/Inspector";
import { Quality } from "./components/Quality";

type Tab = "inspector" | "arena" | "documents" | "audit" | "connector" | "quality";

export function App() {
  const [config, setConfig] = useState<AppConfig | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>("inspector");
  const [incoming, setIncoming] = useState<{ text: string; nonce: number } | null>(null);

  useEffect(() => {
    fetchConfig().then(setConfig, (e) => setError(String(e)));
  }, []);

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <span className="logo" aria-hidden>
            <span />
          </span>
          <span className="brand-name">Freshgate</span>
          <span className="brand-tag">stale-answer detector</span>
        </div>
        <nav className="tabs">
          <button className={tab === "inspector" ? "is-on" : ""} onClick={() => setTab("inspector")}>
            Inspector
          </button>
          <button className={tab === "arena" ? "is-on" : ""} onClick={() => setTab("arena")}>
            Arena
          </button>
          {config?.documents?.available !== false && (
            <button className={tab === "documents" ? "is-on" : ""} onClick={() => setTab("documents")}>
              Documents
            </button>
          )}
          <button className={tab === "audit" ? "is-on" : ""} onClick={() => setTab("audit")}>
            Site audit
          </button>
          <button className={tab === "connector" ? "is-on" : ""} onClick={() => setTab("connector")}>
            Connect
          </button>
          <button className={tab === "quality" ? "is-on" : ""} onClick={() => setTab("quality")}>
            Quality
          </button>
        </nav>
        <span className="spacer" />
        {config &&
          (config.demo ? (
            <span className="status status-demo" title="No API keys configured: bundled fixtures about a fictional company">
              Demo mode · offline fixtures
            </span>
          ) : (
            <span className="status status-live" title={`fallbacks: ${config.fallbacks.join(", ") || "none"}`}>
              <span className="live-dot" /> Live · Bifrost → {config.models.judge}
              {config.jury ? ` + ${config.jury}` : ""} · Tavily {config.search.depth}
            </span>
          ))}
      </header>

      <main>
        {error && <div className="banner banner-err">Backend unreachable: {error}. Start it with `make run` in the repo root.</div>}
        {!config && !error && <p className="muted pad">Connecting to the backend…</p>}
        {config?.warnings?.map((w) => (
          <div key={w} className="banner banner-top">
            {w}
          </div>
        ))}
        {config && (
          <>
            <div hidden={tab !== "inspector"}>
              <Inspector config={config} incoming={incoming} />
            </div>
            <div hidden={tab !== "arena"}>
              <Arena config={config} />
            </div>
            <div hidden={tab !== "documents"}>
              <Documents config={config} />
            </div>
            <div hidden={tab !== "audit"}>
              <Audit config={config} />
            </div>
            {tab === "quality" && <Quality />}
            <div hidden={tab !== "connector"}>
              <Connector
                config={config}
                onInspect={(text) => {
                  setIncoming({ text, nonce: Date.now() });
                  setTab("inspector");
                }}
              />
            </div>
          </>
        )}
      </main>
    </div>
  );
}
