import type { Stats } from "../api";
import { fmt } from "../api";

export function Cards({ s }: { s: Stats }) {
  const errRate = s.total_requests
    ? ((100 * s.total_errors) / s.total_requests).toFixed(1)
    : "0.0";
  const cards: [string, string, string?][] = [
    ["Requests", fmt(s.total_requests)],
    ["Routed", fmt(s.routed_requests), "auto/dynamic"],
    ["Bypass", fmt(s.bypass_requests), "named model"],
    ["Errors", fmt(s.total_errors), errRate + "%"],
    ["Models used", fmt(s.models.length)],
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
