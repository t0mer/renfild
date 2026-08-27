# Grafana dashboard

`renfild-dashboard.json` is an overview of the server's Prometheus metrics:
who is talking, where the time goes per pipeline stage, and how confident the
speaker matcher is. The last one is the panel worth watching — it is the same
number `speaker.default_threshold` is compared against, so it tells you whether
your threshold is set sensibly for your household and microphone.

## Scraping

The server exposes `/metrics` on its normal listen address, unauthenticated,
like the rest of the API. Point Prometheus at it:

```yaml
scrape_configs:
  - job_name: renfild
    scrape_interval: 30s
    static_configs:
      - targets: ["renfild.lan:8080"]
```

Metrics only appear once they have a value, so a freshly started server
publishes just the two unlabelled histograms until it has handled an utterance.

## Importing

Grafana → Dashboards → New → Import → Upload JSON file, then pick your
Prometheus data source. The dashboard has no hard-coded data source UID; it
takes one from the `datasource` variable at the top.

The `speaker` variable filters most panels. It is populated from the labels
Prometheus has actually seen, so it fills in as people talk to the assistant.

## What is here

| Panel | Reads |
|---|---|
| Utterances, Denied, Stage errors | `renfild_utterances_total`, `renfild_stage_errors_total` |
| Unrecognised voices | share of `renfild_utterances_total{speaker="unknown"}` |
| End-to-end latency | `renfild_utterance_duration_seconds` |
| Where the time goes | `renfild_stage_duration_seconds` by `stage` |
| Speaker match confidence | `renfild_speaker_match_confidence` |

Stage labels are `speakerid`, `stt`, `intent`, `tts`, plus `speakerid_second`
for the extra embedding taken when a match lands close to the threshold.

The History page in the web UI covers the same ground per utterance, with the
transcript attached. This dashboard is for trends: a threshold that has drifted,
a Whisper endpoint that got slower, a stage that started failing overnight.
