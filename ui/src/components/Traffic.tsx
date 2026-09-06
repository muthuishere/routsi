import type { Stats } from "../api";
import { fmt } from "../api";

export function Cards({ s }: { s: Stats }) {
  const errRate = s.total_requests
    ? ((100 * s.total_errors) / s.total_requests).toFixed(1)
    : "0.0";
	const providers = new Map<string, number>();
	for (const model of s.models) providers.set(model.provider ?? "unknown", (providers.get(model.provider ?? "unknown") ?? 0) + model.requests);
	const topProvider = [...providers.entries()].sort((a, b) => b[1] - a[1])[0];
  const cards: [string, string, string?][] = [
    ["Requests", fmt(s.total_requests)],
    ["Total tokens", fmt(s.total_tokens), `${fmt(s.prompt_tokens)} in · ${fmt(s.completion_tokens)} out · estimated`],
    ["Avg latency", fmt(s.avg_latency_ms) + "ms", `max ${fmt(s.max_latency_ms)}ms`],
    ["Errors", fmt(s.total_errors), errRate + "%"],
    ["Models used", fmt(s.models.length), `${fmt(s.routed_requests)} routed · ${fmt(s.bypass_requests)} direct`],
	["Top provider", topProvider?.[0] ?? "—", topProvider ? `${fmt(topProvider[1])} requests` : "no traffic"],
    ["S3 events", s.analytics?.enabled ? fmt(s.analytics.uploaded) : "off", s.analytics?.enabled ? `${s.analytics.queued} queued · ${s.analytics.spooled_batches} spooled` : "analytics disabled"],
  ];
  return (
    <div className="cards">
      {cards.map(([k, v, sub]) => (
        <div className="card" key={k}>
          <div className="k">{k}</div>
          <div className="v">
            {v} {sub ? <small>{sub}</small> : null}
          </div>
        </div>
      ))}
    </div>
  );
}

export function ModelTable({ s }: { s: Stats }) {
  if (!s.models.length)
    return (
      <div className="empty">
        No requests yet. Point an OpenAI client at this server and send one.
      </div>
    );

  const max = Math.max(...s.models.map((m) => m.requests));
  return (
    <table>
      <thead>
        <tr>
          <th>Model</th>
          <th>Requests</th>
          <th>Escalations</th>
          <th>Tokens</th>
		  <th>Input</th>
		  <th>Output</th>
          <th>Avg</th>
          <th>Max</th>
          <th>Errors</th>
        </tr>
      </thead>
      <tbody>
        {s.models.map((m) => {
          const tok = m.prompt_tokens + m.completion_tokens;
          const w = Math.max(2, Math.round((100 * m.requests) / max));
          return (
            <tr key={m.model}>
              <td>
                <div className="model">{m.model}</div>
                <div className="prov">{m.provider ?? ""}</div>
                <div className="bar" style={{ width: w + "%" }} />
              </td>
              <td>{fmt(m.requests)}</td>
              <td>{m.escalations ? <span className="tag">{m.escalations}</span> : "—"}</td>
              <td>{tok ? fmt(tok) : "—"}</td>
			  <td>{m.prompt_tokens ? fmt(m.prompt_tokens) : "—"}</td>
			  <td>{m.completion_tokens ? fmt(m.completion_tokens) : "—"}</td>
              <td>
                {m.avg_latency_ms}
                <small style={{ color: "var(--mut)" }}>ms</small>
              </td>
              <td>
                {m.max_latency_ms}
                <small style={{ color: "var(--mut)" }}>ms</small>
              </td>
              <td className={m.errors ? "err" : ""}>{m.errors || "—"}</td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
