import type { Worker } from "../api";
import { dur } from "../api";

const STATE_COLOR: Record<Worker["state"], string> = {
  online: "var(--ok)",
  busy: "var(--warn)",
  stale: "var(--mut)",
  reserved: "var(--line)",
};

export function Workers({ workers }: { workers: Worker[] }) {
  if (!workers.length) return null;
  const rows = [...workers].sort((a, b) => (a.name < b.name ? -1 : 1));
  return (
    <>
      <div className="section-title">Workers</div>
      <table style={{ marginBottom: 20 }}>
        <thead>
          <tr>
            <th>Queue</th>
            <th>Last seen</th>
            <th>In-flight</th>
            <th>Served</th>
            <th>Errors</th>
            <th style={{ textAlign: "left" }}>Last error</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((w) => (
            <tr key={w.name}>
              <td>
                <span
                  className="wdot"
                  style={{ background: STATE_COLOR[w.state] ?? "var(--mut)" }}
                />
                <b>{w.name}</b> <span className="prov">{w.state}</span>
              </td>
              <td>{w.last_seen_sec < 0 ? "—" : dur(w.last_seen_sec) + " ago"}</td>
              <td>{w.in_flight || "—"}</td>
              <td>{w.served || "—"}</td>
              <td className={w.errored ? "err" : ""}>{w.errored || "—"}</td>
              <td className="prov" style={{ textAlign: "left" }}>
                {w.last_error ?? ""}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}
