/**
 * Typed client for the Renfild REST API.
 *
 * Every UI action goes through here; there is no direct fetch anywhere else, so
 * the set of endpoints the UI depends on is visible in one file.
 */

export type Role = "any" | "kid" | "member" | "owner";

export interface Speaker {
  id: number;
  name: string;
  role: Role;
  threshold?: number | null;
  samples: number;
  created_at: string;
  updated_at: string;
}

export interface Enrollment {
  id: number;
  speaker_id: number;
  duration_s: number;
  label: string;
  created_at: string;
  similarity: number;
}

export interface EnrollmentResult {
  accepted: boolean;
  similarity: number;
  reason?: string;
  id?: number;
  samples: number;
  target: number;
  duration_s: number;
}

export interface Identity {
  name: string;
  role: Role;
  score: number;
  threshold: number;
  known: boolean;
  runner_up?: string;
  runner_up_score?: number;
  speaker_id?: number;
}

export interface Intent {
  id: number;
  name: string;
  enabled: boolean;
  match_type: "exact" | "contains" | "regex";
  patterns: string[];
  min_role: Role;
  handler: "reply" | "webhook" | "llm";
  handler_config: Record<string, unknown>;
  priority: number;
  created_at: string;
  updated_at: string;
}

export interface Utterance {
  id: number;
  ts: string;
  satellite_id: string;
  speaker: string;
  confidence: number;
  runner_up?: string;
  runner_up_score?: number;
  transcript: string;
  intent: string;
  reply: string;
  allowed: boolean;
  error?: string;
  audio_path?: string;
  latency_ms_total: number;
  latency_ms_stt: number;
  latency_ms_spk: number;
  latency_ms_intent: number;
  latency_ms_tts: number;
}

export interface Stats {
  total: number;
  last_24h: number;
  by_speaker: Record<string, number>;
  by_intent: Record<string, number>;
  avg_latency_ms: number;
  speaker_count: number;
  intent_count: number;
  unknown_rate_pct: number;
}

export interface RuntimeSettings {
  speaker_threshold: number;
  unknown_policy: "restricted" | "deny" | "allow";
  low_confidence_margin: number;
  llm_fallback: boolean;
  min_sample_similarity: number;
  enrollment_samples: number;
}

export interface StaticSettings {
  listen: string;
  db: string;
  audio_retention: string;
  audio_dir: string;
  whisper_url: string;
  whisper_api: string;
  whisper_language: string;
  embedder_url: string;
  ollama_url: string;
  ollama_model: string;
  piper_binary: string;
  piper_voice: string;
}

export interface Settings {
  runtime: RuntimeSettings;
  static: StaticSettings;
}

export interface IntentTestResult {
  normalized: string;
  result: {
    intent: string;
    handler: string;
    reply: string;
    allowed: boolean;
    matched: boolean;
  };
  error?: string;
}

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: {
      ...(init?.body instanceof FormData ? {} : { "Content-Type": "application/json" }),
      ...init?.headers,
    },
  });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try {
      const body = await response.json();
      if (body?.error) message = body.error;
    } catch {
      /* keep the status line */
    }
    throw new ApiError(message, response.status);
  }
  if (response.status === 204) return undefined as T;
  const type = response.headers.get("Content-Type") ?? "";
  if (type.includes("application/json")) return (await response.json()) as T;
  return (await response.blob()) as unknown as T;
}

export const api = {
  stats: () => request<Stats>("/api/ui/stats"),

  speakers: {
    list: () => request<Speaker[]>("/api/ui/speakers"),
    create: (payload: { name: string; role: Role; threshold?: number | null }) =>
      request<Speaker>("/api/ui/speakers", { method: "POST", body: JSON.stringify(payload) }),
    update: (id: number, payload: { name: string; role: Role; threshold?: number | null }) =>
      request<Speaker>(`/api/ui/speakers/${id}`, { method: "PUT", body: JSON.stringify(payload) }),
    remove: (id: number) => request<void>(`/api/ui/speakers/${id}`, { method: "DELETE" }),
    enrollments: (id: number) => request<Enrollment[]>(`/api/ui/speakers/${id}/enrollments`),
    enroll: (id: number, audio: Blob, label: string) => {
      const form = new FormData();
      form.append("audio", audio, "sample.wav");
      form.append("label", label);
      return request<EnrollmentResult>(`/api/ui/speakers/${id}/enrollments`, {
        method: "POST",
        body: form,
      });
    },
    removeEnrollment: (id: number, enrollmentId: number) =>
      request<void>(`/api/ui/speakers/${id}/enrollments/${enrollmentId}`, { method: "DELETE" }),
    identify: (audio: Blob) => {
      const form = new FormData();
      form.append("audio", audio, "test.wav");
      return request<{ identity: Identity; scores: Record<string, number>; duration_s: number }>(
        "/api/ui/speakers/identify",
        { method: "POST", body: form },
      );
    },
  },

  intents: {
    list: () => request<Intent[]>("/api/ui/intents"),
    create: (payload: Partial<Intent>) =>
      request<Intent>("/api/ui/intents", { method: "POST", body: JSON.stringify(payload) }),
    update: (id: number, payload: Partial<Intent>) =>
      request<Intent>(`/api/ui/intents/${id}`, { method: "PUT", body: JSON.stringify(payload) }),
    remove: (id: number) => request<void>(`/api/ui/intents/${id}`, { method: "DELETE" }),
    reorder: (order: number[]) =>
      request<Intent[]>("/api/ui/intents/reorder", {
        method: "POST",
        body: JSON.stringify({ order }),
      }),
    test: (payload: { transcript: string; speaker: string; role: Role; known?: boolean }) =>
      request<IntentTestResult>("/api/ui/test/intent", {
        method: "POST",
        body: JSON.stringify(payload),
      }),
  },

  history: (params: { limit?: number; offset?: number; speaker?: string; intent?: string; q?: string }) => {
    const query = new URLSearchParams();
    Object.entries(params).forEach(([key, value]) => {
      if (value !== undefined && value !== "") query.set(key, String(value));
    });
    return request<{ items: Utterance[]; total: number; limit: number; offset: number }>(
      `/api/ui/history?${query.toString()}`,
    );
  },

  settings: {
    get: () => request<Settings>("/api/ui/settings"),
    update: (payload: RuntimeSettings) =>
      request<Settings>("/api/ui/settings", { method: "PUT", body: JSON.stringify(payload) }),
  },

  testTTS: (text: string) =>
    request<Blob>("/api/ui/test/tts", { method: "POST", body: JSON.stringify({ text }) }),
};
