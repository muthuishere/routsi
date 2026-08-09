import type { Config } from "../api";

export function Configuration({ cfg }: { cfg: Config }) {
  const tlsClass =
    cfg.tls === "off" ? "off" : cfg.tls === "mtls" ? "warn" : "on";
  const kv: [string, React.ReactNode][] = [
    ["Listen", cfg.listen],
    ["Default model", cfg.default],
    [
      "Auth",
      <span className={"pill " + (cfg.auth ? "on" : "off")}>
        {cfg.auth ? "on" : "off"}
      </span>,
    ],
    ["TLS", <span className={"pill " + tlsClass}>{cfg.tls}</span>],
    ["Decider", cfg.decider],
  ];

  const tiers = Object.entries(cfg.tiers ?? {});
  const groups = Object.entries(cfg.dynamic_groups ?? {});

  return (
    <>
      <div className="section-title">Configuration</div>
      <div className="panel">
        <div className="kvgrid">
          {kv.map(([k, v]) => (
            <div key={k}>
              <div className="k">{k}</div>
              <div className="v">{v}</div>
            </div>
          ))}
        </div>

        <div className="subhead">Tiers</div>
        <div className="chiplist">
          {tiers.length ? (
            tiers.map(([lvl, m]) => (
              <div className="chip" key={lvl}>
                <b>{lvl}</b>
                {m}
              </div>
            ))
          ) : (
            <span className="prov">none configured</span>
          )}
        </div>

        <div className="subhead">Dynamic groups</div>
        <div className="chiplist">
          {groups.length ? (
            groups.map(([name, levels]) => (
              <div className="chip" key={name}>
                <b>{name}</b>
                {Object.entries(levels)
                  .map(([l, m]) => `${l}=${m}`)
                  .join(", ")}
              </div>
            ))
          ) : (
            <span className="prov">none configured</span>
          )}
        </div>

        <div className="subhead">Model catalog</div>
        {cfg.models?.length ? (
          <table>
            <thead>
              <tr>
                <th style={{ textAlign: "left" }}>Model</th>
                <th style={{ textAlign: "left" }}>Type</th>
                <th style={{ textAlign: "left" }}>Provider</th>
              </tr>
            </thead>
            <tbody>
              {cfg.models.map((m) => (
                <tr key={m.name}>
                  <td>{m.name}</td>
                  <td>{m.type}</td>
                  <td className="prov">{m.provider ?? ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <span className="prov">no models configured</span>
        )}
      </div>
    </>
  );
}
