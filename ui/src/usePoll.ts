import { useEffect, useState } from "react";
import { get, Unauthorized } from "./api";

// Poll a JSON endpoint on an interval. A failed poll keeps the last good value
// on screen — a dashboard that blanks on one dropped request is worse than a
// dashboard that is three seconds stale.
export function usePoll<T>(path: string, everyMs: number) {
  const [data, setData] = useState<T | null>(null);
  const [denied, setDenied] = useState(false);

  useEffect(() => {
    let live = true;
    const tick = async () => {
      try {
        const d = await get<T>(path);
        if (live) {
          setData(d);
          setDenied(false);
        }
      } catch (e) {
        if (live && e instanceof Unauthorized) setDenied(true);
      }
    };
    tick();
    const id = setInterval(tick, everyMs);
    return () => {
      live = false;
      clearInterval(id);
    };
  }, [path, everyMs]);

  return { data, denied };
}
