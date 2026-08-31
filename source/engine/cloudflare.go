package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	cfUserAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"
	cfOrigin    = "https://speed.cloudflare.com"
	cfReferer   = "https://speed.cloudflare.com/"
)

type CloudflareProvider struct {
	client *http.Client
}

func NewCloudflareProvider() *CloudflareProvider {
	return &CloudflareProvider{
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

func (c *CloudflareProvider) Type() string {
	return "cloudflare"
}

func (c *CloudflareProvider) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", cfUserAgent)
	req.Header.Set("Origin", cfOrigin)
	req.Header.Set("Referer", cfReferer)
}

func (c *CloudflareProvider) ListServers(ctx context.Context) ([]Server, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://speed.cloudflare.com/__down?bytes=0", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return []Server{
			{
				ID:      "auto",
				Type:    "cloudflare",
				Name:    "Cloudflare Edge (Anycast)",
				Sponsor: "Cloudflare",
				Country: "Global",
				Host:    "speed.cloudflare.com",
				URL:     "https://speed.cloudflare.com",
			},
		}, nil
	}
	defer resp.Body.Close()

	colo := resp.Header.Get("colo")
	city := resp.Header.Get("city")
	country := resp.Header.Get("country")

	displayName := "Cloudflare Edge"
	if colo != "" {
		if city != "" {
			displayName = fmt.Sprintf("Cloudflare %s (%s, %s)", colo, city, country)
		} else {
			displayName = fmt.Sprintf("Cloudflare %s (%s)", colo, country)
		}
	}

	return []Server{
		{
			ID:      "auto",
			Type:    "cloudflare",
			Name:    displayName,
			Sponsor: "Cloudflare",
			Country: country,
			Host:    "speed.cloudflare.com",
			URL:     "https://speed.cloudflare.com",
		},
	}, nil
}

func (c *CloudflareProvider) Run(ctx context.Context, s Server, onProgress func(Progress)) (*Result, error) {
	startTime := time.Now()

	onProgress(Progress{
		Stage:    "ping",
		Progress: 0.05,
	})

	pingSamples := make([]float64, 0, 10)
	for i := 0; i < 10; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		reqStart := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://speed.cloudflare.com/__down?bytes=0", nil)
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)

		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		sample := float64(time.Since(reqStart).Microseconds()) / 1000.0
		pingSamples = append(pingSamples, sample)

		onProgress(Progress{
			Stage:    "ping",
			Progress: 0.05 + float64(i+1)*0.015,
			Ping:     math.Round(sample*10) / 10,
		})
		time.Sleep(50 * time.Millisecond)
	}

	sort.Float64s(pingSamples)
	var pingMs, jitterMs float64
	if len(pingSamples) > 0 {
		var sum float64
		for _, v := range pingSamples {
			sum += v
		}
		pingMs = sum / float64(len(pingSamples))

		var jitterSum float64
		for i := 1; i < len(pingSamples); i++ {
			jitterSum += math.Abs(pingSamples[i] - pingSamples[i-1])
		}
		jitterMs = jitterSum / float64(len(pingSamples)-1)
	}

	pingMs = math.Round(pingMs*10) / 10
	jitterMs = math.Round(jitterMs*10) / 10

	onProgress(Progress{
		Stage:    "download",
		Progress: 0.20,
		Ping:     pingMs,
		Jitter:   jitterMs,
	})

	var downloadedBytes atomic.Int64
	var activeDlMbps atomic.Uint64
	downloadCtx, cancelDl := context.WithTimeout(ctx, 8*time.Second)
	defer cancelDl()

	dlStart := time.Now()
	var dlWg sync.WaitGroup
	workers := 4

	for w := 0; w < workers; w++ {
		dlWg.Add(1)
		go func() {
			defer dlWg.Done()
			buf := make([]byte, 64*1024)
			for {
				select {
				case <-downloadCtx.Done():
					return
				default:
				}

				req, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, "https://speed.cloudflare.com/__down?bytes=10000000", nil)
				if err != nil {
					return
				}
				c.setHeaders(req)

				resp, err := c.client.Do(req)
				if err != nil {
					time.Sleep(100 * time.Millisecond)
					continue
				}

				if resp.StatusCode != http.StatusOK {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					time.Sleep(100 * time.Millisecond)
					continue
				}

				for {
					n, err := resp.Body.Read(buf)
					if n > 0 {
						downloadedBytes.Add(int64(n))
					}
					if err != nil {
						break
					}
				}
				resp.Body.Close()
			}
		}()
	}

	dlTicker := time.NewTicker(200 * time.Millisecond)
	for {
		stop := false
		select {
		case <-downloadCtx.Done():
			stop = true
		case <-dlTicker.C:
		}

		elapsed := time.Since(dlStart).Seconds()
		if elapsed > 0 {
			total := downloadedBytes.Load()
			mbps := (float64(total) * 8) / (elapsed * 1000000)
			activeDlMbps.Store(math.Float64bits(mbps))

			p := 0.20 + (math.Min(elapsed, 8.0)/8.0)*0.40
			onProgress(Progress{
				Stage:     "download",
				Progress:  math.Round(p*100) / 100,
				Ping:      pingMs,
				Jitter:    jitterMs,
				Download:  math.Round(mbps*100) / 100,
				BytesDone: total,
			})
		}
		if stop {
			break
		}
	}
	dlTicker.Stop()
	dlWg.Wait()

	finalDlMbps := math.Float64frombits(activeDlMbps.Load())
	finalDlMbps = math.Round(finalDlMbps*100) / 100

	onProgress(Progress{
		Stage:    "upload",
		Progress: 0.60,
		Ping:     pingMs,
		Jitter:   jitterMs,
		Download: finalDlMbps,
	})

	var uploadedBytes atomic.Int64
	var activeUlMbps atomic.Uint64
	uploadCtx, cancelUl := context.WithTimeout(ctx, 8*time.Second)
	defer cancelUl()

	payloadSize := 1024 * 1024
	randomPayload := make([]byte, payloadSize)
	rand.Read(randomPayload)

	ulStart := time.Now()
	var ulWg sync.WaitGroup

	for w := 0; w < workers; w++ {
		ulWg.Add(1)
		go func() {
			defer ulWg.Done()
			for {
				select {
				case <-uploadCtx.Done():
					return
				default:
				}

				reader := bytes.NewReader(randomPayload)
				req, err := http.NewRequestWithContext(uploadCtx, http.MethodPost, "https://speed.cloudflare.com/__up", reader)
				if err != nil {
					return
				}
				c.setHeaders(req)
				req.Header.Set("Content-Type", "application/octet-stream")

				resp, err := c.client.Do(req)
				if err != nil {
					time.Sleep(100 * time.Millisecond)
					continue
				}
				if resp.StatusCode == http.StatusOK {
					uploadedBytes.Add(int64(payloadSize))
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}

	ulTicker := time.NewTicker(200 * time.Millisecond)
	for {
		stop := false
		select {
		case <-uploadCtx.Done():
			stop = true
		case <-ulTicker.C:
		}

		elapsed := time.Since(ulStart).Seconds()
		if elapsed > 0 {
			total := uploadedBytes.Load()
			mbps := (float64(total) * 8) / (elapsed * 1000000)
			activeUlMbps.Store(math.Float64bits(mbps))

			p := 0.60 + (math.Min(elapsed, 8.0)/8.0)*0.38
			onProgress(Progress{
				Stage:     "upload",
				Progress:  math.Round(p*100) / 100,
				Ping:      pingMs,
				Jitter:    jitterMs,
				Download:  finalDlMbps,
				Upload:    math.Round(mbps*100) / 100,
				BytesDone: total,
			})
		}
		if stop {
			break
		}
	}
	ulTicker.Stop()
	ulWg.Wait()

	finalUlMbps := math.Float64frombits(activeUlMbps.Load())
	finalUlMbps = math.Round(finalUlMbps*100) / 100

	totalDuration := math.Round(time.Since(startTime).Seconds()*10) / 10

	return &Result{
		Timestamp:       time.Now().Unix(),
		Provider:        "cloudflare",
		ServerID:        s.ID,
		ServerName:      s.Name,
		ServerCountry:   s.Country,
		ServerSponsor:   s.Sponsor,
		ServerHost:      s.Host,
		DownloadMbps:    finalDlMbps,
		UploadMbps:      finalUlMbps,
		PingMs:          pingMs,
		JitterMs:        jitterMs,
		DurationS:       totalDuration,
		BytesDownloaded: downloadedBytes.Load(),
		BytesUploaded:   uploadedBytes.Load(),
		CreatedAt:       time.Now().Unix(),
	}, nil
}
