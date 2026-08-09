import type { Config, Decision, Stats, Worker } from "./api";
import { dur } from "./api";
import { usePoll } from "./usePoll";
import { Endpoint } from "./components/Endpoint";
import { Configuration } from "./components/Configuration";
import { Cards, ModelTable } from "./components/Traffic";
import { Workers } from "./components/Workers";
import { Decisions } from "./components/Decisions";

export function App() {
  // Traffic moves fast, config barely at all — poll them accordingly.
  const stats = usePoll<Stats>("/stats", 3000);
  const config = usePoll<Config>("/config", 15000);
  const workers = usePoll<{ workers: Worker[] }>("/v1/workers", 3000);
  const audit = usePoll<{ decisions: Decision[] }>("/audit?limit=25", 3000);

  const denied = stats.denied || config.denied;

  return (
    <>
      <header>
        <span className="dot" />
        <h1>routsi</h1>
        <span className="sub">
          {stats.data ? "up " + dur(stats.data.uptime_seconds) : "—"}
        </span>
      </header>

      <div className="wrap">
        {denied ? (
          <div className="empty">
            401 — this server requires a token. Open <code>/?token=YOUR_TOKEN</code>.
          </div>
        ) : (
          <>
            {config.data && <Endpoint cfg={config.data} />}
            {config.data && <Configuration cfg={config.data} />}
            {stats.data && <Cards s={stats.data} />}
            <Workers workers={workers.data?.workers ?? []} />
            {stats.data && <ModelTable s={stats.data} />}
            <Decisions rows={audit.data?.decisions ?? []} />
          </>
        )}
      </div>

      <footer>auto-refreshing every 3s</footer>
    </>
  );
}
