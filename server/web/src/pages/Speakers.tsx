import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Mic, Square, Trash2, UserPlus, Wand2 } from "lucide-react";

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
  Field,
  Input,
  Select,
  Table,
  Td,
  Th,
} from "@/components/ui/primitives";
import { api, type Enrollment, type Identity, type Role, type Speaker } from "@/lib/api";
import { usePolling } from "@/lib/hooks";
import { Recorder } from "@/lib/recorder";
import { formatTime, score } from "@/lib/utils";

/** The guided enrollment script. Variety matters more than length. */
const PROMPTS = [
  { label: "wake-1", text: "Say the wake word, normally." },
  { label: "wake-2", text: "Say the wake word again, from where you usually stand." },
  { label: "sentence-1", text: '"Turn on the lights in the living room, please."' },
  { label: "sentence-2", text: '"What is the weather going to be like tomorrow?"' },
  { label: "free", text: "Say anything you like for three or four seconds." },
];

export default function Speakers() {
  const { data, error, reload } = usePolling(() => api.speakers.list(), 60_000);
  const [selected, setSelected] = useState<Speaker>();
  const [formError, setFormError] = useState<string>();

  const speakers = data ?? [];

  const create = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    try {
      const created = await api.speakers.create({
        name: String(form.get("name") ?? "").trim(),
        role: (form.get("role") as Role) ?? "member",
      });
      event.currentTarget.reset();
      setFormError(undefined);
      await reload();
      setSelected(created);
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    }
  };

  const remove = async (speaker: Speaker) => {
    if (!confirm(`Delete ${speaker.name} and all their voice samples?`)) return;
    await api.speakers.remove(speaker.id);
    if (selected?.id === speaker.id) setSelected(undefined);
    await reload();
  };

  return (
    <div className="flex flex-col gap-5">
      <ErrorNote>{error}</ErrorNote>

      <Card>
        <CardHeader>
          <CardTitle>Speakers</CardTitle>
          <CardDescription>
            Renfild recognises a voice by comparing it with the samples recorded here. Five good
            samples are enough; more variety beats more repetitions.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {speakers.length === 0 ? (
            <Empty>Nobody is enrolled yet. Add the first person below.</Empty>
          ) : (
            <Table>
              <thead>
                <tr>
                  <Th>Name</Th>
                  <Th>Role</Th>
                  <Th>Samples</Th>
                  <Th>Threshold</Th>
                  <Th>Updated</Th>
                  <Th />
                </tr>
              </thead>
              <tbody>
                {speakers.map((speaker) => (
                  <tr key={speaker.id}>
                    <Td className="font-medium">{speaker.name}</Td>
                    <Td>
                      <Badge>{speaker.role}</Badge>
                    </Td>
                    <Td>
                      <Badge tone={speaker.samples >= 3 ? "good" : "warn"}>{speaker.samples}</Badge>
                    </Td>
                    <Td className="text-[var(--color-ink-muted)]">
                      {speaker.threshold != null ? speaker.threshold.toFixed(2) : "default"}
                    </Td>
                    <Td className="whitespace-nowrap text-[var(--color-ink-muted)]">
                      {formatTime(speaker.updated_at)}
                    </Td>
                    <Td>
                      <div className="flex justify-end gap-2">
                        <Button size="sm" onClick={() => setSelected(speaker)}>
                          <Wand2 size={14} /> Enroll
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => void remove(speaker)}>
                          <Trash2 size={14} />
                        </Button>
                      </div>
                    </Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}

          <form className="grid gap-3 sm:grid-cols-[1fr_10rem_auto]" onSubmit={create}>
            <Field label="Name">
              <Input name="name" placeholder="Tomer" required />
            </Field>
            <Field label="Role">
              <Select name="role" defaultValue="member">
                <option value="owner">owner</option>
                <option value="member">member</option>
                <option value="kid">kid</option>
              </Select>
            </Field>
            <div className="flex items-end">
              <Button type="submit" variant="primary" className="w-full">
                <UserPlus size={16} /> Add
              </Button>
            </div>
          </form>
          <ErrorNote>{formError}</ErrorNote>
        </CardContent>
      </Card>

      {selected ? (
        <EnrollmentWizard speaker={selected} onChanged={() => void reload()} onClose={() => setSelected(undefined)} />
      ) : null}

      <VoiceTest />
    </div>
  );
}

function EnrollmentWizard({
  speaker,
  onChanged,
  onClose,
}: {
  speaker: Speaker;
  onChanged: () => void;
  onClose: () => void;
}) {
  const [samples, setSamples] = useState<Enrollment[]>([]);
  const [step, setStep] = useState(0);
  const [status, setStatus] = useState<string>();
  const [error, setError] = useState<string>();
  const [recording, setRecording] = useState(false);
  const recorder = useRef(new Recorder());

  const load = useCallback(async () => {
    try {
      setSamples(await api.speakers.enrollments(speaker.id));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [speaker.id]);

  useEffect(() => {
    void load();
    setStep(0);
    setStatus(undefined);
  }, [load]);

  const prompt = PROMPTS[Math.min(step, PROMPTS.length - 1)];

  const start = async () => {
    setError(undefined);
    setStatus(undefined);
    try {
      await recorder.current.start();
      setRecording(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const stop = async () => {
    setRecording(false);
    try {
      const wav = await recorder.current.stop();
      const result = await api.speakers.enroll(speaker.id, wav, prompt.label);
      if (result.accepted) {
        setStatus(
          `Sample kept (${result.duration_s.toFixed(1)}s, similarity ${score(result.similarity)}). ` +
            `${result.samples} of ${result.target}.`,
        );
        setStep((value) => Math.min(value + 1, PROMPTS.length - 1));
      } else {
        setStatus(undefined);
        setError(result.reason ?? "sample rejected");
      }
      await load();
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const drop = async (enrollment: Enrollment) => {
    await api.speakers.removeEnrollment(speaker.id, enrollment.id);
    await load();
    onChanged();
  };

  return (
    <Card>
      <CardHeader>
        <div className="flex items-start justify-between gap-4">
          <div>
            <CardTitle>Enrolling {speaker.name}</CardTitle>
            <CardDescription>
              Record where you normally speak from, at your normal volume. A sample that does not
              resemble the others is rejected on the spot.
            </CardDescription>
          </div>
          <Button size="sm" variant="ghost" onClick={onClose}>
            Done
          </Button>
        </div>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="rounded-lg border border-[var(--color-line)] bg-[var(--color-surface-muted)] p-4">
          <div className="text-xs font-medium tracking-wide text-[var(--color-ink-muted)] uppercase">
            Step {Math.min(step + 1, PROMPTS.length)} of {PROMPTS.length} · {prompt.label}
          </div>
          <p className="mt-1 text-lg">{prompt.text}</p>
          <div className="mt-3 flex gap-2">
            {recording ? (
              <Button variant="danger" onClick={() => void stop()}>
                <Square size={16} /> Stop and save
              </Button>
            ) : (
              <Button variant="primary" onClick={() => void start()}>
                <Mic size={16} /> Record
              </Button>
            )}
            <Button variant="ghost" onClick={() => setStep((value) => (value + 1) % PROMPTS.length)}>
              Skip prompt
            </Button>
          </div>
        </div>

        {status ? (
          <div className="rounded-lg border border-emerald-500/40 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-600 dark:text-emerald-400">
            {status}
          </div>
        ) : null}
        <ErrorNote>{error}</ErrorNote>

        {samples.length === 0 ? (
          <Empty>No samples yet.</Empty>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>Recorded</Th>
                <Th>Prompt</Th>
                <Th>Length</Th>
                <Th>Similarity to voice print</Th>
                <Th />
              </tr>
            </thead>
            <tbody>
              {samples.map((sample) => (
                <tr key={sample.id}>
                  <Td className="whitespace-nowrap text-[var(--color-ink-muted)]">
                    {formatTime(sample.created_at)}
                  </Td>
                  <Td>{sample.label || "—"}</Td>
                  <Td>{sample.duration_s ? `${sample.duration_s.toFixed(1)}s` : "—"}</Td>
                  <Td>
                    <Badge tone={sample.similarity >= 0.6 ? "good" : sample.similarity >= 0.4 ? "warn" : "bad"}>
                      {score(sample.similarity)}
                    </Badge>
                  </Td>
                  <Td>
                    <div className="flex justify-end">
                      <Button size="sm" variant="ghost" onClick={() => void drop(sample)}>
                        <Trash2 size={14} />
                      </Button>
                    </div>
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

function VoiceTest() {
  const [recording, setRecording] = useState(false);
  const [result, setResult] = useState<{ identity: Identity; scores: Record<string, number> }>();
  const [error, setError] = useState<string>();
  const recorder = useRef(new Recorder());

  const ranked = useMemo(
    () => Object.entries(result?.scores ?? {}).sort((a, b) => b[1] - a[1]),
    [result],
  );

  const toggle = async () => {
    setError(undefined);
    if (!recording) {
      try {
        await recorder.current.start();
        setRecording(true);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
      return;
    }
    setRecording(false);
    try {
      const wav = await recorder.current.stop();
      setResult(await api.speakers.identify(wav));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Test my voice</CardTitle>
        <CardDescription>
          Records a few seconds and reports who Renfild thinks you are — nothing is stored.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div>
          <Button variant={recording ? "danger" : "primary"} onClick={() => void toggle()}>
            {recording ? <Square size={16} /> : <Mic size={16} />}
            {recording ? "Stop and identify" : "Record"}
          </Button>
        </div>
        <ErrorNote>{error}</ErrorNote>
        {result ? (
          <div className="flex flex-col gap-3">
            <div className="flex items-center gap-2 text-lg">
              <span>{result.identity.known ? result.identity.name : "unknown"}</span>
              <Badge tone={result.identity.known ? "good" : "warn"}>
                {score(result.identity.score)} vs threshold {score(result.identity.threshold)}
              </Badge>
            </div>
            <Table>
              <thead>
                <tr>
                  <Th>Speaker</Th>
                  <Th>Similarity</Th>
                </tr>
              </thead>
              <tbody>
                {ranked.map(([name, value]) => (
                  <tr key={name}>
                    <Td>{name}</Td>
                    <Td>{score(value)}</Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
