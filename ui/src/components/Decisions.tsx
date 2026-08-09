import type { Decision } from "../api";
import { fmt } from "../api";

export function Decisions({ rows }: { rows: Decision[] }) {
  if (!rows.length) return null;
  return (
    <>
      <div className="section-title">Recent decisions</div>
      <table>
        <thead>
          <tr>
            <th style={{ textAlign: "left" }}>Time</th>
            <th style={{ textAlign: "left" }}>Requested &rarr; Selected</th>
            <th>Level</th>
            <th style={{ textAlign: "left" }}>Source</th>
            <th>Status</th>
            <th>Latency</th>
            <th>Tokens</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((x, i) => {
            const time = x.time?.split("T")[1]?.replace("Z", "") ?? x.time;
            const tok = x.tokens?.total ?? 0;
            return (
              <tr key={i}>
                <td className="prov">{time}</td>
                <td>
                  {x.requested_model} <span className="prov">&rarr;</span>{" "}
                  <b>{x.selected_model}</b>
                </td>
                <td>{x.level ? <span className="tag">{x.level}</span> : "—"}</td>
                <td className="prov">{x.source}</td>
                <td className={x.status >= 400 || !x.status ? "err" : ""}>
                  {x.status || "—"}
                </td>
                <td>
                  {x.latency_ms}
                  <small style={{ color: "var(--mut)" }}>ms</small>
                </td>
                <td>{tok ? fmt(tok) : "—"}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </>
  );
}
