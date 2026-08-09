import { useState } from "react";
import type { Config } from "../api";
import { TOKEN } from "../api";

// "How do I call this thing" — the whole point of the panel is that someone
// who lands on the dashboard can copy one line and be talking to routsi.

function snippets(base: string, key: string, model: string) {
  return {
    curl: `curl ${base}/chat/completions \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"${model}","messages":[{"role":"user","content":"Hello"}]}'`,

    python: `from openai import OpenAI

client = OpenAI(base_url="${base}", api_key="${key}")
r = client.chat.completions.create(
    model="${model}",
    messages=[{"role": "user", "content": "Hello"}],
)
print(r.choices[0].message.content)`,

    node: `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${base}", apiKey: "${key}" });
const r = await client.chat.completions.create({
  model: "${model}",
  messages: [{ role: "user", content: "Hello" }],
});
console.log(r.choices[0].message.content);`,

    env: `export OPENAI_BASE_URL="${base}"
export OPENAI_API_KEY="${key}"
# any OpenAI-compatible tool now routes through routsi`,
  };
}

function Copy({ text }: { text: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      className="copy"
      onClick={() => {
        navigator.clipboard.writeText(text);
        setDone(true);
        setTimeout(() => setDone(false), 1200);
      }}
    >
      {done ? "copied" : "copy"}
    </button>
  );
}

export function Endpoint({ cfg }: { cfg: Config }) {
  const [tab, setTab] = useState<keyof ReturnType<typeof snippets>>("curl");

  const base = location.origin + "/v1";
  const key = TOKEN ?? (cfg.auth ? "YOUR_TOKEN" : "not-needed");
  // Lead with a routing model — that is what makes routsi routsi.
  const model =
    cfg.models?.find((m) => m.type === "dynamic")?.name ?? cfg.default;
  const snips = snippets(base, key, model);

  return (
    <>
      <div className="section-title">Endpoint</div>
      <div className="panel">
        <div className="endpoint">
          <code>{base}</code>
          <Copy text={base} />
          <span className={"pill " + (cfg.auth ? "warn" : "on")}>
            {cfg.auth ? "token required" : "no auth"}
          </span>
        </div>
        <div className="prov" style={{ fontSize: 12 }}>
          OpenAI-compatible. Point any OpenAI SDK or tool at this base URL and
          ask for a model below — <b>{model}</b> routes by task difficulty, a
          concrete name bypasses routing.
        </div>

        <div className="tabs">
          {(Object.keys(snips) as (keyof typeof snips)[]).map((k) => (
            <span
              key={k}
              className={"tab" + (k === tab ? " sel" : "")}
              onClick={() => setTab(k)}
            >
              {k}
            </span>
          ))}
          <span style={{ flex: 1 }} />
          <Copy text={snips[tab]} />
        </div>
        <pre className="snip">{snips[tab]}</pre>

        <div className="subhead">Ask for any of these as "model"</div>
        <div className="chiplist">
          {(cfg.models ?? []).map((m) => (
            <div className="chip" key={m.name}>
              {m.type === "dynamic" ? <b>routes</b> : null}
              {m.name}
            </div>
          ))}
        </div>
      </div>
    </>
  );
}
