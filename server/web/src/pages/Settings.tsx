import { useEffect, useState } from "react";
import { Volume2 } from "lucide-react";

import {
  Button,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  ErrorNote,
  Field,
  Input,
  Select,
  Switch,
  Table,
  Td,
  Th,
} from "@/components/ui/primitives";
import { api, type RuntimeSettings, type Settings } from "@/lib/api";

export default function SettingsPage() {
  const [settings, setSettings] = useState<Settings>();
  const [draft, setDraft] = useState<RuntimeSettings>();
  const [status, setStatus] = useState<string>();
  const [error, setError] = useState<string>();
  const [ttsText, setTtsText] = useState("Renfild is listening.");

  useEffect(() => {
    api
      .settings.get()
      .then((value) => {
        setSettings(value);
        setDraft(value.runtime);
      })
      .catch((err) => setError(err.message));
  }, []);

  const save = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!draft) return;
    try {
      const saved = await api.settings.update(draft);
      setSettings(saved);
      setDraft(saved.runtime);
      setStatus("Saved. New values apply to the next utterance.");
      setError(undefined);
    } catch (err) {
      setStatus(undefined);
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const speak = async () => {
    try {
      const blob = await api.testTTS(ttsText);
      new Audio(URL.createObjectURL(blob)).play();
      setError(undefined);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  if (!draft || !settings) {
    return <ErrorNote>{error ?? "Loading…"}</ErrorNote>;
  }

  return (
    <div className="flex flex-col gap-5">
      <Card>
        <CardHeader>
          <CardTitle>Recognition and routing</CardTitle>
          <CardDescription>
            These take effect immediately and survive a restart. Thresholds always need tuning per
            household — check the History page for the scores your microphone actually produces.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form className="grid gap-4 md:grid-cols-2" onSubmit={save}>
            <Field label="Speaker threshold" hint="Cosine similarity floor. Start at 0.45.">
              <Input
                type="number"
                step="0.01"
                min="-1"
                max="1"
                value={draft.speaker_threshold}
                onChange={(event) => setDraft({ ...draft, speaker_threshold: Number(event.target.value) })}
              />
            </Field>
            <Field label="Unknown voice policy" hint="restricted: only 'any' intents · deny: silence · allow: treat as member">
              <Select
                value={draft.unknown_policy}
                onChange={(event) =>
                  setDraft({ ...draft, unknown_policy: event.target.value as RuntimeSettings["unknown_policy"] })
                }
              >
                <option value="restricted">restricted</option>
                <option value="deny">deny</option>
                <option value="allow">allow</option>
              </Select>
            </Field>
            <Field label="Borderline margin" hint="Within this of the threshold, the command audio gets a vote too.">
              <Input
                type="number"
                step="0.01"
                min="0"
                max="0.5"
                value={draft.low_confidence_margin}
                onChange={(event) => setDraft({ ...draft, low_confidence_margin: Number(event.target.value) })}
              />
            </Field>
            <Field label="Enrollment samples" hint="How many recordings the wizard asks for.">
              <Input
                type="number"
                min="1"
                max="20"
                value={draft.enrollment_samples}
                onChange={(event) => setDraft({ ...draft, enrollment_samples: Number(event.target.value) })}
              />
            </Field>
            <Field label="Minimum sample similarity" hint="Enrollment samples below this are rejected.">
              <Input
                type="number"
                step="0.01"
                min="0"
                max="1"
                value={draft.min_sample_similarity}
                onChange={(event) => setDraft({ ...draft, min_sample_similarity: Number(event.target.value) })}
              />
            </Field>
            <div className="flex items-center gap-3 pt-6">
              <Switch
                checked={draft.llm_fallback}
                onChange={(value) => setDraft({ ...draft, llm_fallback: value })}
                label="LLM fallback"
              />
              <span className="text-sm">Send unmatched commands to the local LLM</span>
            </div>
            <div className="md:col-span-2 flex items-center gap-3">
              <Button type="submit" variant="primary">
                Save
              </Button>
              {status ? <span className="text-sm text-emerald-600 dark:text-emerald-400">{status}</span> : null}
            </div>
          </form>
          <div className="mt-3">
            <ErrorNote>{error}</ErrorNote>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Test the voice</CardTitle>
          <CardDescription>Synthesises a phrase with Piper and plays it in this browser.</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex gap-2">
            <Input dir="auto" value={ttsText} onChange={(event) => setTtsText(event.target.value)} />
            <Button onClick={() => void speak()}>
              <Volume2 size={16} /> Speak
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>From the config file</CardTitle>
          <CardDescription>
            Read-only here. Change these in <code>/opt/renfild/etc/server.yaml</code> and restart the
            service.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Table>
            <thead>
              <tr>
                <Th>Setting</Th>
                <Th>Value</Th>
              </tr>
            </thead>
            <tbody>
              {Object.entries(settings.static).map(([key, value]) => (
                <tr key={key}>
                  <Td className="font-medium">{key}</Td>
                  <Td className="font-mono text-xs break-all">{String(value)}</Td>
                </tr>
              ))}
            </tbody>
          </Table>
        </CardContent>
      </Card>
    </div>
  );
}
