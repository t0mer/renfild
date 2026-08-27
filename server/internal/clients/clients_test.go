package clients

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWhisperOpenAIShape(t *testing.T) {
	var gotPath, gotModel, gotLanguage, gotAuth, gotFilename string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parsing multipart: %v", err)
		}
		gotModel = r.FormValue("model")
		gotLanguage = r.FormValue("language")
		if _, header, err := r.FormFile("file"); err == nil {
			gotFilename = header.Filename
		} else {
			t.Errorf("missing 'file' part: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"  turn on the lights  "}`))
	}))
	defer server.Close()

	client := NewWhisper(server.URL, WhisperAPIOpenAI, "whisper-1", "he", "secret", 5*time.Second)
	text, err := client.Transcribe(context.Background(), []byte("RIFFaudio"))
	if err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if text != "turn on the lights" {
		t.Fatalf("text = %q (should be trimmed)", text)
	}
	if gotPath != "/v1/audio/transcriptions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotModel != "whisper-1" || gotLanguage != "he" {
		t.Fatalf("model = %q, language = %q", gotModel, gotLanguage)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotFilename != "command.wav" {
		t.Fatalf("filename = %q", gotFilename)
	}
}

func TestWhisperASRShape(t *testing.T) {
	var gotPath, gotQuery, gotField string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		r.ParseMultipartForm(1 << 20)
		if _, _, err := r.FormFile("audio_file"); err == nil {
			gotField = "audio_file"
		}
		w.Write([]byte(`{"text":"מה השעה"}`))
	}))
	defer server.Close()

	client := NewWhisper(server.URL, WhisperAPIASR, "", "he", "", 5*time.Second)
	text, err := client.Transcribe(context.Background(), []byte("RIFFaudio"))
	if err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if text != "מה השעה" {
		t.Fatalf("text = %q", text)
	}
	if gotPath != "/asr" {
		t.Fatalf("path = %q", gotPath)
	}
	for _, want := range []string{"task=transcribe", "output=json", "language=he"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query %q is missing %q", gotQuery, want)
		}
	}
	if gotField != "audio_file" {
		t.Fatal("the ASR shape must send the audio as 'audio_file'")
	}
}

func TestWhisperOmitsTheLanguageHintWhenAuto(t *testing.T) {
	var gotLanguage string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		gotLanguage = r.FormValue("language")
		w.Write([]byte(`{"text":"hello"}`))
	}))
	defer server.Close()

	client := NewWhisper(server.URL, WhisperAPIOpenAI, "whisper-1", "auto", "", 5*time.Second)
	if _, err := client.Transcribe(context.Background(), []byte("audio")); err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if gotLanguage != "" {
		t.Fatalf("language = %q, want it omitted so the model detects it", gotLanguage)
	}
}

func TestWhisperAcceptsAPlainTextResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("hello from a plain endpoint"))
	}))
	defer server.Close()

	client := NewWhisper(server.URL, WhisperAPIOpenAI, "whisper-1", "auto", "", 5*time.Second)
	text, err := client.Transcribe(context.Background(), []byte("audio"))
	if err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if text != "hello from a plain endpoint" {
		t.Fatalf("text = %q", text)
	}
}

func TestWhisperErrors(t *testing.T) {
	t.Run("no audio", func(t *testing.T) {
		client := NewWhisper("http://localhost", WhisperAPIOpenAI, "", "auto", "", time.Second)
		if _, err := client.Transcribe(context.Background(), nil); err == nil {
			t.Fatal("expected an error for empty audio")
		}
	})

	t.Run("server error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "model is loading", http.StatusServiceUnavailable)
		}))
		defer server.Close()

		client := NewWhisper(server.URL, WhisperAPIOpenAI, "", "auto", "", time.Second)
		_, err := client.Transcribe(context.Background(), []byte("audio"))
		if err == nil || !strings.Contains(err.Error(), "model is loading") {
			t.Fatalf("error = %v, want the server's message", err)
		}
	})

	t.Run("unknown api shape", func(t *testing.T) {
		client := NewWhisper("http://localhost", "grpc", "", "auto", "", time.Second)
		if _, err := client.Transcribe(context.Background(), []byte("audio")); err == nil {
			t.Fatal("expected an error for an unknown API shape")
		}
	})
}

func TestEmbedder(t *testing.T) {
	var gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		if string(body) != "RIFFaudio" {
			t.Errorf("body = %q", body)
		}
		w.Write([]byte(`{"embedding":[0.1,0.2,0.3],"duration_s":2.5,"dims":3}`))
	}))
	defer server.Close()

	client := NewEmbedder(server.URL, 5*time.Second)
	vector, duration, err := client.Embed(context.Background(), []byte("RIFFaudio"))
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if len(vector) != 3 || vector[0] != 0.1 {
		t.Fatalf("vector = %v", vector)
	}
	if duration != 2.5 {
		t.Fatalf("duration = %v", duration)
	}
	if gotContentType != "audio/wav" {
		t.Fatalf("content type = %q", gotContentType)
	}
}

func TestEmbedderErrors(t *testing.T) {
	t.Run("no audio", func(t *testing.T) {
		client := NewEmbedder("http://localhost", time.Second)
		if _, _, err := client.Embed(context.Background(), nil); err == nil {
			t.Fatal("expected an error for empty audio")
		}
	})

	t.Run("rejected audio", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "audio too short", http.StatusUnprocessableEntity)
		}))
		defer server.Close()

		client := NewEmbedder(server.URL, time.Second)
		_, _, err := client.Embed(context.Background(), []byte("audio"))
		if err == nil || !strings.Contains(err.Error(), "audio too short") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("empty embedding", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"embedding":[],"duration_s":1}`))
		}))
		defer server.Close()

		client := NewEmbedder(server.URL, time.Second)
		if _, _, err := client.Embed(context.Background(), []byte("audio")); err == nil {
			t.Fatal("expected an error for an empty embedding")
		}
	})
}

func TestEmbedderHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	if err := NewEmbedder(server.URL, time.Second).Health(context.Background()); err != nil {
		t.Fatalf("Health() error: %v", err)
	}
}

func TestOllamaChat(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Write([]byte(`{"message":{"role":"assistant","content":"  Blue light scatters more.  "}}`))
	}))
	defer server.Close()

	client := NewOllama(server.URL, "qwen2.5:7b", 5*time.Second)
	answer, err := client.Chat(context.Background(), "be brief", "why is the sky blue")
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if answer != "Blue light scatters more." {
		t.Fatalf("answer = %q", answer)
	}
	for _, want := range []string{`"model":"qwen2.5:7b"`, `"stream":false`, `"role":"system"`, "why is the sky blue"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body %q is missing %q", gotBody, want)
		}
	}
}

func TestOllamaOmitsAnEmptySystemPrompt(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Write([]byte(`{"message":{"content":"ok"}}`))
	}))
	defer server.Close()

	if _, err := NewOllama(server.URL, "m", time.Second).Chat(context.Background(), "  ", "hi"); err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if strings.Contains(gotBody, `"role":"system"`) {
		t.Fatalf("empty system prompt was sent: %s", gotBody)
	}
}

func TestOllamaErrors(t *testing.T) {
	t.Run("api error field", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"error":"model not found"}`))
		}))
		defer server.Close()

		_, err := NewOllama(server.URL, "missing", time.Second).Chat(context.Background(), "", "hi")
		if err == nil || !strings.Contains(err.Error(), "model not found") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("empty answer", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"message":{"content":"   "}}`))
		}))
		defer server.Close()

		if _, err := NewOllama(server.URL, "m", time.Second).Chat(context.Background(), "", "hi"); err == nil {
			t.Fatal("expected an error for an empty answer")
		}
	})
}

// fakePiper writes a shell script that behaves like the real binary: it reads
// text on stdin and writes a WAV to stdout.
func fakePiper(t *testing.T, body string) (binary, voice string) {
	t.Helper()
	dir := t.TempDir()
	binary = filepath.Join(dir, "piper")
	if err := os.WriteFile(binary, []byte(body), 0o700); err != nil {
		t.Fatalf("writing fake piper: %v", err)
	}
	voice = filepath.Join(dir, "voice.onnx")
	if err := os.WriteFile(voice, []byte("model"), 0o600); err != nil {
		t.Fatalf("writing fake voice: %v", err)
	}
	return binary, voice
}

func TestPiperSynthesize(t *testing.T) {
	binary, voice := fakePiper(t, "#!/bin/sh\ncat > /dev/null\nprintf 'RIFFWAVE'\n")

	piper := NewPiper(PiperOptions{Binary: binary, Voice: voice, Timeout: 5 * time.Second})
	audio, err := piper.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize() error: %v", err)
	}
	if string(audio) != "RIFFWAVE" {
		t.Fatalf("audio = %q", audio)
	}
}

func TestPiperPassesTheVoiceAndSpeakerArguments(t *testing.T) {
	binary, voice := fakePiper(t, "#!/bin/sh\ncat > /dev/null\necho \"$@\"\n")

	piper := NewPiper(PiperOptions{Binary: binary, Voice: voice, SpeakerID: 3, Timeout: 5 * time.Second,
		ExtraArgs: []string{"--length_scale", "1.1"}})
	out, err := piper.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize() error: %v", err)
	}
	args := string(out)
	for _, want := range []string{"--model " + voice, "--output_file -", "--speaker 3", "--length_scale 1.1"} {
		if !strings.Contains(args, want) {
			t.Fatalf("arguments %q are missing %q", args, want)
		}
	}
}

func TestPiperErrors(t *testing.T) {
	t.Run("empty text", func(t *testing.T) {
		binary, voice := fakePiper(t, "#!/bin/sh\nprintf 'x'\n")
		if _, err := NewPiper(PiperOptions{Binary: binary, Voice: voice, Timeout: time.Second}).Synthesize(context.Background(), "  "); err == nil {
			t.Fatal("expected an error for empty text")
		}
	})

	t.Run("missing binary", func(t *testing.T) {
		piper := NewPiper(PiperOptions{Binary: "/nonexistent/piper", Voice: "/nonexistent/voice.onnx", Timeout: time.Second})
		if err := piper.Available(); err == nil {
			t.Fatal("expected Available() to report the missing binary")
		}
		if _, err := piper.Synthesize(context.Background(), "hello"); err == nil {
			t.Fatal("expected Synthesize() to fail without a binary")
		}
	})

	t.Run("binary fails", func(t *testing.T) {
		binary, voice := fakePiper(t, "#!/bin/sh\ncat > /dev/null\necho 'voice not found' >&2\nexit 1\n")
		_, err := NewPiper(PiperOptions{Binary: binary, Voice: voice, Timeout: time.Second}).Synthesize(context.Background(), "hello")
		if err == nil || !strings.Contains(err.Error(), "voice not found") {
			t.Fatalf("error = %v, want piper's stderr", err)
		}
	})

	t.Run("binary produces nothing", func(t *testing.T) {
		binary, voice := fakePiper(t, "#!/bin/sh\ncat > /dev/null\n")
		if _, err := NewPiper(PiperOptions{Binary: binary, Voice: voice, Timeout: time.Second}).Synthesize(context.Background(), "hello"); err == nil {
			t.Fatal("expected an error when no audio is produced")
		}
	})
}
