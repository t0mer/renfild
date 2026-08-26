import { useEffect, useState } from "react";
import { Search } from "lucide-react";

import {
  Badge,
  Button,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  Empty,
  ErrorNote,
  Input,
  Table,
  Td,
  Th,
} from "@/components/ui/primitives";
import { api, type Utterance } from "@/lib/api";
import { formatTime, ms, score } from "@/lib/utils";

const PAGE_SIZE = 25;

export default function HistoryPage() {
  const [items, setItems] = useState<Utterance[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [query, setQuery] = useState("");
  const [applied, setApplied] = useState("");
  const [error, setError] = useState<string>();

  useEffect(() => {
    api
      .history({ limit: PAGE_SIZE, offset, q: applied })
      .then((page) => {
        setItems(page.items);
        setTotal(page.total);
        setError(undefined);
      })
      .catch((err) => setError(err.message));
  }, [offset, applied]);

  const search = (event: React.FormEvent) => {
    event.preventDefault();
    setOffset(0);
    setApplied(query);
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>History</CardTitle>
        <CardDescription>
          Every utterance, with per-stage timings. This is the first place to look when the wrong
          person gets recognised or a reply takes too long.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <form className="flex gap-2" onSubmit={search}>
          <Input
            dir="auto"
            placeholder="Search transcripts and replies"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
          <Button type="submit">
            <Search size={16} /> Search
          </Button>
        </form>

        <ErrorNote>{error}</ErrorNote>

        {items.length === 0 ? (
          <Empty>Nothing recorded yet.</Empty>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>Time</Th>
                <Th>Satellite</Th>
                <Th>Speaker</Th>
                <Th>Transcript</Th>
                <Th>Intent</Th>
                <Th>Reply</Th>
                <Th>Latency</Th>
                <Th>Audio</Th>
              </tr>
            </thead>
            <tbody>
              {items.map((item) => (
                <tr key={item.id}>
                  <Td className="whitespace-nowrap text-[var(--color-ink-muted)]">{formatTime(item.ts)}</Td>
                  <Td className="text-[var(--color-ink-muted)]">{item.satellite_id || "—"}</Td>
                  <Td>
                    <div className="flex items-center gap-2">
                      {item.speaker || "unknown"}
                      <Badge tone={item.speaker === "unknown" ? "warn" : "good"}>{score(item.confidence)}</Badge>
                    </div>
                    {item.runner_up ? (
                      <div className="text-xs text-[var(--color-ink-muted)]">
                        vs {item.runner_up} {score(item.runner_up_score)}
                      </div>
                    ) : null}
                  </Td>
                  <Td dir="auto" className="max-w-xs">{item.transcript || "—"}</Td>
                  <Td>
                    <Badge tone={item.error ? "bad" : item.allowed ? "neutral" : "warn"}>{item.intent || "—"}</Badge>
                    {item.error ? <div className="mt-1 text-xs text-red-500">{item.error}</div> : null}
                  </Td>
                  <Td dir="auto" className="max-w-xs text-[var(--color-ink-muted)]">{item.reply || "—"}</Td>
                  <Td className="whitespace-nowrap text-xs text-[var(--color-ink-muted)]">
                    <div>total {ms(item.latency_ms_total)}</div>
                    <div>
                      spk {item.latency_ms_spk} · stt {item.latency_ms_stt} · int {item.latency_ms_intent} · tts{" "}
                      {item.latency_ms_tts}
                    </div>
                  </Td>
                  <Td>
                    {item.audio_path ? (
                      // Retention is off by default; when it is on, the recording plays here.
                      <audio controls preload="none" src={`/api/ui/history/${item.id}/audio`} className="h-8" />
                    ) : (
                      <span className="text-xs text-[var(--color-ink-muted)]">not stored</span>
                    )}
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}

        <div className="flex items-center justify-between text-sm text-[var(--color-ink-muted)]">
          <span>
            {total === 0 ? "0" : `${offset + 1}–${Math.min(offset + PAGE_SIZE, total)}`} of {total}
          </span>
          <div className="flex gap-2">
            <Button size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}>
              Previous
            </Button>
            <Button size="sm" disabled={offset + PAGE_SIZE >= total} onClick={() => setOffset(offset + PAGE_SIZE)}>
              Next
            </Button>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
