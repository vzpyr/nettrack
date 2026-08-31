# NetTrack

Self-hosted internet speed tracker and network monitor written in Go

## Features

- **Multi-Engine Speedtesting:** Manual or scheduled tests using Cloudflare, LibreSpeed, or Ookla
- **Real-Time Streaming:** Live ping, jitter, download, and upload telemetry over SSE with cancellation
- **Analytics & Percentiles:** Interactive bandwidth/latency time-series charts (uPlot) with min, avg, peak, and p90/p95 stats
- **Automated Scheduling:** Embedded in-process cron scheduler with automatic retention pruning
- **History & Filtering:** Dynamic filtering by provider and server, failure tracking, and data volume metrics

## Deployment

### Docker (Recommended)

```bash
cp .env.example .env
cp docker-compose.example.yml docker-compose.yml
docker compose up -d
```

### From Source

Requires Go 1.24+:

```bash
cd source
go build -ldflags="-s -w" -o ../nettrack .
```

Binary output: `nettrack`

## Configuration

### Environment Variables

| Variable            | Default | Description                                               |
| ------------------- | ------- | --------------------------------------------------------- |
| `NETTRACK_PORT`     | `8080`  | Server listen port                                        |
| `NETTRACK_PASSWORD` | `""`    | Access password (leave empty to disable authentication)   |
| `NETTRACK_DATA_DIR` | `data`  | Directory where SQLite database (`nettrack.db`) is stored |

## License

MIT
