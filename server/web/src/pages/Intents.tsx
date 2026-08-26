import { useEffect, useState } from "react";
import { ArrowDown, ArrowUp, Play, Plus, Trash2 } from "lucide-react";

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
  Switch,
  Table,
  Td,
  Textarea,
  Th,
} from "@/components/ui/primitives";
import { api, type Intent, type IntentTestResult, type Role } from "@/lib/api";
import { usePolling } from "@/lib/hooks";

const BLANK: Partial<Intent> = {
  name: "",
  enabled: true,
  match_type: "contains",
  patterns: [],
  min_role: "member",
  handler: "reply",
  handler_config: { template: "" },
  priority: 100,
};

export default function Intents() {
  const { data, error, reload } = usePolling(() => api.intents.list(), 60_000);
  const [editing, setEditing] = useState<Partial<Intent>>();
  const [formError, setFormError] = useState<string>();

  const intents = data ?? [];

  const save = async (rule: Partial<Intent>) => {
    try {
      if (rule.id) await api.intents.update(rule.id, rule);
      else await api.intents.create(rule);
      setEditing(undefined);
      setFormError(undefined);
      await reload();
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    }
  };

  const move = async (index: number, direction: -1 | 1) => {
    const next = [...intents];
    const target = index + direction;
    if (target < 0 || target >= next.length) return;
    [next[index], next[target]] = [next[target], next[index]];
    await api.intents.reorder(next.map((rule) => rule.id));
    await reload();
  };

  const remove = async (rule: Intent) => {
    if (!confirm(`Delete intent "${rule.name}"?`)) return;
    await api.intents.remove(rule.id);
    await reload();
  };

  const toggle = async (rule: Intent) => {
    await api.intents.update(rule.id, { ...rule, enabled: !rule.enabled });
    await reload();
  };

  return (
    <div className="flex flex-col gap-5">
      <ErrorNote>{error}</ErrorNote>

      <Card>
        <CardHeader>
          <div className="flex items-start justify-between gap-4">
            <div>
              <CardTitle>Intent rules</CardTitle>
              <CardDescription>
                Checked from the top down; the first enabled rule that matches wins. Anything that
                matches nothing falls through to the LLM, if that is switched on in Settings.
              </CardDescription>
            </div>
            <Button variant="primary" size="sm" onClick={() => setEditing({ ...BLANK })}>
              <Plus size={16} /> New rule
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {intents.length === 0 ? (
            <Empty>No rules yet.</Empty>
          ) : (
            <Table>
              <thead>
                <tr>
                  <Th>Order</Th>
                  <Th>Name</Th>
                  <Th>Match</Th>
                  <Th>Patterns</Th>
                  <Th>Role floor</Th>
                  <Th>Handler</Th>
                  <Th>On</Th>
                  <Th />
                </tr>
              </thead>
              <tbody>
                {intents.map((rule, index) => (
                  <tr key={rule.id}>
                    <Td>
                      <div className="flex items-center gap-1">
                        <Button size="sm" variant="ghost" onClick={() => void move(index, -1)} aria-label="Move up">
                          <ArrowUp size={14} />
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => void move(index, 1)} aria-label="Move down">
                          <ArrowDown size={14} />
                        </Button>
                        <span className="text-xs text-[var(--color-ink-muted)]">{rule.priority}</span>
                      </div>
                    </Td>
                    <Td className="font-medium">{rule.name}</Td>
                    <Td>
                      <Badge>{rule.match_type}</Badge>
                    </Td>
                    <Td dir="auto" className="max-w-xs text-[var(--color-ink-muted)]">
                      {rule.patterns.join(" · ")}
                    </Td>
                    <Td>
                      <Badge tone={rule.min_role === "any" ? "warn" : "neutral"}>{rule.min_role}</Badge>
                    </Td>
                    <Td>
                      <Badge>{rule.handler}</Badge>
                    </Td>
                    <Td>
                      <Switch checked={rule.enabled} onChange={() => void toggle(rule)} label={`Enable ${rule.name}`} />
                    </Td>
                    <Td>
                      <div className="flex justify-end gap-1">
                        <Button size="sm" onClick={() => setEditing(rule)}>
                          Edit
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => void remove(rule)}>
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

      {editing ? (
        <IntentEditor
          rule={editing}
          error={formError}
          onCancel={() => {
            setEditing(undefined);
            setFormError(undefined);
          }}
          onSave={save}
        />
      ) : null}

      <IntentTester />
    </div>
  );
}

function IntentEditor({
  rule,
  error,
  onCancel,
  onSave,
}: {
  rule: Partial<Intent>;
  error?: string;
  onCancel: () => void;
  onSave: (rule: Partial<Intent>) => void;
}) {
  const [draft, setDraft] = useState(rule);
  const [config, setConfig] = useState(() => JSON.stringify(rule.handler_config ?? {}, null, 2));
  const [configError, setConfigError] = useState<string>();

  useEffect(() => {
    setDraft(rule);
    setConfig(JSON.stringify(rule.handler_config ?? {}, null, 2));
  }, [rule]);

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    let parsed: Record<string, unknown>;
    try {
      parsed = JSON.parse(config || "{}");
      setConfigError(undefined);
    } catch (err) {
      setConfigError(`handler config: ${err instanceof Error ? err.message : String(err)}`);
      return;
    }
    onSave({ ...draft, handler_config: parsed });
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>{draft.id ? `Edit "${rule.name}"` : "New intent"}</CardTitle>
        <CardDescription>
          Templates can use <code>{"{{.Speaker}}"}</code>, <code>{"{{.Transcript}}"}</code> and{" "}
          <code>{'{{.Now.Format "15:04"}}'}</code>.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form className="grid gap-4 md:grid-cols-2" onSubmit={submit}>
          <Field label="Name">
            <Input
              value={draft.name ?? ""}
              onChange={(event) => setDraft({ ...draft, name: event.target.value })}
              required
            />
          </Field>
          <Field label="Priority" hint="Lower runs first.">
            <Input
              type="number"
              value={draft.priority ?? 100}
              onChange={(event) => setDraft({ ...draft, priority: Number(event.target.value) })}
            />
          </Field>
          <Field label="Match type">
            <Select
              value={draft.match_type ?? "contains"}
              onChange={(event) => setDraft({ ...draft, match_type: event.target.value as Intent["match_type"] })}
            >
              <option value="exact">exact</option>
              <option value="contains">contains</option>
              <option value="regex">regex</option>
            </Select>
          </Field>
          <Field label="Minimum role" hint="'any' also lets unrecognised voices through.">
            <Select
              value={draft.min_role ?? "member"}
              onChange={(event) => setDraft({ ...draft, min_role: event.target.value as Role })}
            >
              <option value="any">any</option>
              <option value="kid">kid</option>
              <option value="member">member</option>
              <option value="owner">owner</option>
            </Select>
          </Field>
          <Field label="Patterns" hint="One per line. Hebrew is fine.">
            <Textarea
              dir="auto"
              rows={4}
              value={(draft.patterns ?? []).join("\n")}
              onChange={(event) =>
                setDraft({
                  ...draft,
                  patterns: event.target.value.split("\n").map((line) => line.trim()).filter(Boolean),
                })
              }
            />
          </Field>
          <Field label="Handler">
            <Select
              value={draft.handler ?? "reply"}
              onChange={(event) => setDraft({ ...draft, handler: event.target.value as Intent["handler"] })}
            >
              <option value="reply">reply</option>
              <option value="webhook">webhook</option>
              <option value="llm">llm</option>
            </Select>
          </Field>
          <div className="md:col-span-2">
            <Field
              label="Handler configuration (JSON)"
              hint='reply: {"template": "..."} · webhook: {"url","method","body","speak_response"} · llm: {"system_prompt","max_words"}'
            >
              <Textarea rows={6} className="font-mono text-xs" value={config} onChange={(event) => setConfig(event.target.value)} />
            </Field>
          </div>
          <div className="md:col-span-2 flex items-center gap-3">
            <Switch
              checked={draft.enabled ?? true}
              onChange={(value) => setDraft({ ...draft, enabled: value })}
              label="Enabled"
            />
            <span className="text-sm">Enabled</span>
            <div className="ml-auto flex gap-2">
              <Button type="button" variant="ghost" onClick={onCancel}>
                Cancel
              </Button>
              <Button type="submit" variant="primary">
                Save
              </Button>
            </div>
          </div>
          <div className="md:col-span-2">
            <ErrorNote>{configError ?? error}</ErrorNote>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function IntentTester() {
  const [transcript, setTranscript] = useState("");
  const [speaker, setSpeaker] = useState("");
  const [role, setRole] = useState<Role>("member");
  const [result, setResult] = useState<IntentTestResult>();
  const [error, setError] = useState<string>();

  const run = async (event: React.FormEvent) => {
    event.preventDefault();
    try {
      setResult(await api.intents.test({ transcript, speaker, role, known: speaker !== "" }));
      setError(undefined);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Try a phrase</CardTitle>
        <CardDescription>Routes a transcript as if someone had said it. Nothing is spoken or recorded.</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="grid gap-3 md:grid-cols-[2fr_1fr_10rem_auto]" onSubmit={run}>
          <Field label="Transcript">
            <Input dir="auto" value={transcript} onChange={(event) => setTranscript(event.target.value)} required />
          </Field>
          <Field label="Speaker" hint="Leave blank for an unrecognised voice.">
            <Input value={speaker} onChange={(event) => setSpeaker(event.target.value)} />
          </Field>
          <Field label="Role">
            <Select value={role} onChange={(event) => setRole(event.target.value as Role)}>
              <option value="owner">owner</option>
              <option value="member">member</option>
              <option value="kid">kid</option>
            </Select>
          </Field>
          <div className="flex items-end">
            <Button type="submit" variant="primary" className="w-full">
              <Play size={16} /> Route
            </Button>
          </div>
        </form>

        <ErrorNote>{error ?? result?.error}</ErrorNote>

        {result ? (
          <div className="mt-4 grid gap-2 rounded-lg border border-[var(--color-line)] p-4 text-sm">
            <div>
              <span className="text-[var(--color-ink-muted)]">normalized: </span>
              <code dir="auto">{result.normalized}</code>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Badge tone={result.result.matched ? "good" : "warn"}>
                {result.result.matched ? "matched" : "fell through"}
              </Badge>
              <Badge>{result.result.intent || "—"}</Badge>
              <Badge>{result.result.handler || "—"}</Badge>
              {result.result.allowed ? null : <Badge tone="bad">denied</Badge>}
            </div>
            <div dir="auto">{result.result.reply || <span className="text-[var(--color-ink-muted)]">no spoken reply</span>}</div>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
