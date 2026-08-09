// Typed views of the routsi HTTP surface the dashboard reads.
// Auth: when the server has tokens configured, the operator opens /?token=...
// and every fetch carries it as a bearer header (never stored anywhere).

export const TOKEN = new URLSearchParams(location.search).get("token");

export class Unauthorized extends Error {}

export async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, {
    headers: TOKEN ? { Authorization: "Bearer " + TOKEN } : {},
  });
  if (res.status === 401) throw new Unauthorized(path);
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return (await res.json()) as T;
}

export type ModelStat = {
  model: string;
  provider?: string;
  requests: number;
  escalations: number;
  errors: number;
  prompt_tokens: number;
  completion_tokens: number;
  avg_latency_ms: number;
  max_latency_ms: number;
};

export type Stats = {
  uptime_seconds: number;
  total_requests: number;
  routed_requests: number;
  bypass_requests: number;
  total_errors: number;
  models: ModelStat[];
};

export type ConfigModel = { name: string; type: string; provider?: string };

export type Config = {
  listen: string;
  default: string;
  auth: boolean;
  tls: string;
  decider: string;
  tiers?: Record<string, string>;
  dynamic_groups?: Record<string, Record<string, string>>;
  models?: ConfigModel[];
};

export type Worker = {
  name: string;
  state: "online" | "busy" | "stale" | "reserved";
  last_seen_sec: number;
  in_flight: number;
  served: number;
  errored: number;
  last_error?: string;
};

export type Decision = {
  time: string;
  requested_model: string;
  selected_model: string;
  level?: string;
  source: string;
  status: number;
  latency_ms: number;
  tokens?: { total: number };
};

export const fmt = (n: number): string =>
  n >= 1e6
    ? (n / 1e6).toFixed(1) + "M"
    : n >= 1e3
      ? (n / 1e3).toFixed(1) + "k"
      : String(n);

export function dur(s: number): string {
  const d = Math.floor(s / 86400),
    h = Math.floor((s % 86400) / 3600),
    m = Math.floor((s % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${s % 60}s`;
  return `${s}s`;
}
