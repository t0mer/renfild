import { useEffect, useState } from "react";

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  Badge,
  Empty,
  ErrorNote,
  Table,
  Td,
  Th,
} from "@/components/ui/primitives";
import { api, type Utterance } from "@/lib/api";
import { usePolling } from "@/lib/hooks";
import { formatTime, ms, score } from "@/lib/utils";

/** Live view of what the house has been saying. */
export default function Dashboard() {
  const stats = usePolling(() => api.stats(), 60_000);
  const [recent, setRecent] = useState<Utterance[]>([]);
  const [streamError, setStreamError] = useState<string>();

  useEffect(() => {
    // Seed with history, then let the SSE stream take over.
    void api
      .history({ limit: 15 })
      .then((page) => setRecent(page.items))
      .catch((err) => setStreamError(err.message));

    const source = new EventSource("/api/ui/events");
    source.addEventListener("utterance", (event) => {
      const utterance = JSON.parse((event as MessageEvent).data) as Utterance;
      setRecent((previous) => [utterance, ...previous].slice(0, 25));
      void stats.reload();
    });
    source.onerror = () => setStreamError("live stream disconnected — retrying");
    source.onopen = () => setStreamError(undefined);
    return () => source.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div className="flex flex-col gap-5">
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Stat label="Utterances (24h)" value={stats.data?.last_24h ?? 0} hint={`${stats.data?.total ?? 0} all time`} />
        <Stat label="Average latency" value={ms(stats.data?.avg_latency_ms)} hint="wake audio to spoken reply" />
        <Stat label="Enrolled speakers" value={stats.data?.speaker_count ?? 0} hint={`${stats.data?.intent_count ?? 0} intents`} />
        <Stat
          label="Unrecognised voices"
          value={`${stats.data?.unknown_rate_pct ?? 0}%`}
          hint="lower the threshold if this is high"
        />
      </div>

      <ErrorNote>{stats.error}</ErrorNote>
      <ErrorNote>{streamError}</ErrorNote>

      <Card>
        <CardHeader>
          <CardTitle>Recent utterances</CardTitle>
          <CardDescription>Updates live as the satellites report in.</CardDescription>
        </CardHeader>
        <CardContent>
          {recent.length === 0 ? (
            <Empty>Nothing yet. Say the wake word.</Empty>
          ) : (
            <Table>
              <thead>
                <tr>
                  <Th>Time</Th>
                  <Th>Speaker</Th>
                  <Th>Transcript</Th>
                  <Th>Intent</Th>
                  <Th>Reply</Th>
                  <Th>Latency</Th>
                </tr>
              </thead>
              <tbody>
                {recent.map((item) => (
                  <tr key={`${item.id}-${item.ts}`}>
                    <Td className="whitespace-nowrap text-[var(--color-ink-muted)]">{formatTime(item.ts)}</Td>
                    <Td>
                      <div className="flex items-center gap-2">
                        <span>{item.speaker || "unknown"}</span>
                        <Badge tone={item.speaker === "unknown" ? "warn" : "good"}>{score(item.confidence)}</Badge>
                      </div>
                      {item.runner_up ? (
                        <div className="text-xs text-[var(--color-ink-muted)]">
                          runner-up {item.runner_up} {score(item.runner_up_score)}
                        </div>
                      ) : null}
                    </Td>
                    <Td dir="auto" className="max-w-xs">{item.transcript || "—"}</Td>
                    <Td>
                      <Badge tone={item.error ? "bad" : item.allowed ? "neutral" : "warn"}>
                        {item.intent || "—"}
                      </Badge>
                    </Td>
                    <Td dir="auto" className="max-w-xs text-[var(--color-ink-muted)]">{item.reply || "—"}</Td>
                    <Td className="whitespace-nowrap text-xs text-[var(--color-ink-muted)]">
                      <div>total {ms(item.latency_ms_total)}</div>
                      <div>
                        spk {item.latency_ms_spk} · stt {item.latency_ms_stt} · int {item.latency_ms_intent} · tts{" "}
                        {item.latency_ms_tts}
                      </div>
                    </Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </CardContent>
      </Card>

      <div className="grid gap-4 md:grid-cols-2">
        <Breakdown title="Who has been talking" data={stats.data?.by_speaker} />
        <Breakdown title="Which intents fire" data={stats.data?.by_intent} />
      </div>
    </div>
  );
}

function Stat({ label, value, hint }: { label: string; value: string | number; hint?: string }) {
  return (
    <Card>
      <CardContent className="pt-5">
        <div className="text-sm text-[var(--color-ink-muted)]">{label}</div>
        <div className="mt-1 text-2xl font-semibold">{value}</div>
        {hint ? <div className="mt-1 text-xs text-[var(--color-ink-muted)]">{hint}</div> : null}
      </CardContent>
    </Card>
  );
}

function Breakdown({ title, data }: { title: string; data?: Record<string, number> }) {
  const entries = Object.entries(data ?? {}).sort((a, b) => b[1] - a[1]);
  const max = entries[0]?.[1] ?? 1;
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {entries.length === 0 ? (
          <Empty>No data yet.</Empty>
        ) : (
          entries.map(([key, count]) => (
            <div key={key} className="flex items-center gap-3">
              <span className="w-32 shrink-0 truncate text-sm">{key}</span>
              <div className="h-2 flex-1 rounded-full bg-[var(--color-surface-muted)]">
                <div
                  className="h-2 rounded-full bg-[var(--color-accent)]"
                  style={{ width: `${Math.max(4, (count / max) * 100)}%` }}
                />
              </div>
              <span className="w-10 text-right text-sm text-[var(--color-ink-muted)]">{count}</span>
            </div>
          ))
        )}
      </CardContent>
    </Card>
  );
}
