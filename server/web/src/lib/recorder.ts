/**
 * Browser microphone capture that produces 16 kHz mono PCM WAV.
 *
 * MediaRecorder hands back WebM/Opus, which the embedder's libsndfile cannot
 * read, so the audio is decoded with the Web Audio API, resampled to the rate
 * the models expect and re-encoded as a plain PCM WAV before upload.
 */

export const TARGET_SAMPLE_RATE = 16000;

export class Recorder {
  private media?: MediaRecorder;
  private stream?: MediaStream;
  private chunks: BlobPart[] = [];

  get recording(): boolean {
    return this.media?.state === "recording";
  }

  async start(): Promise<void> {
    this.stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true },
    });
    this.chunks = [];
    this.media = new MediaRecorder(this.stream);
    this.media.ondataavailable = (event) => {
      if (event.data.size > 0) this.chunks.push(event.data);
    };
    this.media.start();
  }

  /** Stop recording and return the captured audio as a 16 kHz mono WAV. */
  async stop(): Promise<Blob> {
    const recorder = this.media;
    if (!recorder) throw new Error("not recording");

    const finished = new Promise<Blob>((resolve) => {
      recorder.onstop = () => resolve(new Blob(this.chunks, { type: recorder.mimeType }));
    });
    recorder.stop();
    const raw = await finished;
    this.stream?.getTracks().forEach((track) => track.stop());
    this.media = undefined;
    this.stream = undefined;
    return encodeWav(await toMono16k(await raw.arrayBuffer()));
  }

  cancel(): void {
    this.media?.stop();
    this.stream?.getTracks().forEach((track) => track.stop());
    this.media = undefined;
    this.stream = undefined;
    this.chunks = [];
  }
}

/** Decode any browser-recorded blob into 16 kHz mono float samples. */
async function toMono16k(data: ArrayBuffer): Promise<Float32Array> {
  const context = new AudioContext();
  try {
    const decoded = await context.decodeAudioData(data.slice(0));
    const offline = new OfflineAudioContext(1, Math.ceil(decoded.duration * TARGET_SAMPLE_RATE), TARGET_SAMPLE_RATE);
    const source = offline.createBufferSource();
    source.buffer = decoded;
    source.connect(offline.destination);
    source.start();
    const rendered = await offline.startRendering();
    return rendered.getChannelData(0);
  } finally {
    await context.close();
  }
}

/** Encode float samples as a 16-bit PCM WAV. */
function encodeWav(samples: Float32Array, sampleRate = TARGET_SAMPLE_RATE): Blob {
  const buffer = new ArrayBuffer(44 + samples.length * 2);
  const view = new DataView(buffer);

  const writeString = (offset: number, text: string) => {
    for (let i = 0; i < text.length; i += 1) view.setUint8(offset + i, text.charCodeAt(i));
  };

  writeString(0, "RIFF");
  view.setUint32(4, 36 + samples.length * 2, true);
  writeString(8, "WAVE");
  writeString(12, "fmt ");
  view.setUint32(16, 16, true); // PCM header size
  view.setUint16(20, 1, true); // format: PCM
  view.setUint16(22, 1, true); // channels
  view.setUint32(24, sampleRate, true);
  view.setUint32(28, sampleRate * 2, true); // byte rate
  view.setUint16(32, 2, true); // block align
  view.setUint16(34, 16, true); // bits per sample
  writeString(36, "data");
  view.setUint32(40, samples.length * 2, true);

  let offset = 44;
  for (let i = 0; i < samples.length; i += 1) {
    const clamped = Math.max(-1, Math.min(1, samples[i]));
    view.setInt16(offset, clamped * 0x7fff, true);
    offset += 2;
  }
  return new Blob([buffer], { type: "audio/wav" });
}
