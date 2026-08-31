package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type LibreSpeedServerItem struct {
	ID          interface{} `json:"id"`
	Name        string      `json:"name"`
	Server      string      `json:"server"`
	DlURL       string      `json:"dlURL"`
	UlURL       string      `json:"ulURL"`
	PingURL     string      `json:"pingURL"`
	GetIPURL    string      `json:"getIpURL"`
	SponsorName string      `json:"sponsorName"`
	SponsorURL  string      `json:"sponsorURL"`
}

type LibreSpeedProvider struct {
	client *http.Client
}

func NewLibreSpeedProvider() *LibreSpeedProvider {
	return &LibreSpeedProvider{
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

func (l *LibreSpeedProvider) Type() string {
	return "librespeed"
}

func (l *LibreSpeedProvider) ListServers(ctx context.Context) ([]Server, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://librespeed.org/backend-servers/servers.json", nil)
	if err != nil {
		return nil, err
	}

	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status from librespeed servers list: %d", resp.StatusCode)
	}

	var rawServers []LibreSpeedServerItem
	if err := json.NewDecoder(resp.Body).Decode(&rawServers); err != nil {
		return nil, err
	}

	servers := make([]Server, 0, len(rawServers))
	for _, item := range rawServers {
		idStr := ""
		switch v := item.ID.(type) {
		case float64:
			idStr = strconv.FormatInt(int64(v), 10)
		case string:
			idStr = v
		}

		u, err := url.Parse(item.Server)
		host := item.Server
		if err == nil && u.Host != "" {
			host = u.Host
		}

		serverURL := strings.TrimRight(item.Server, "/")

		country := ""
		parts := strings.Split(item.Name, ",")
		if len(parts) > 1 {
			country = strings.TrimSpace(strings.Split(parts[1], "(")[0])
		}

		displayName := item.Name
		if idStr != "" {
			displayName = fmt.Sprintf("%s (%s)", item.Name, idStr)
		}

		servers = append(servers, Server{
			ID:      idStr,
			Type:    "librespeed",
			Name:    displayName,
			Sponsor: item.SponsorName,
			Country: country,
			Host:    host,
			URL:     serverURL,
			PingURL: item.PingURL,
			DlURL:   item.DlURL,
			UlURL:   item.UlURL,
		})
	}

	return servers, nil
}

func (l *LibreSpeedProvider) Run(ctx context.Context, s Server, onProgress func(Progress)) (*Result, error) {
	startTime := time.Now()

	onProgress(Progress{
		Stage:    "ping",
		Progress: 0.05,
	})

	pingEndpoint := s.URL + "/" + s.PingURL
	pingSamples := make([]float64, 0, 10)

	for i := 0; i < 10; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		reqStart := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pingEndpoint, nil)
		if err != nil {
			return nil, err
		}
		resp, err := l.client.Do(req)
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

	dlEndpoint := s.URL + "/" + s.DlURL
	sep := "?"
	if strings.Contains(dlEndpoint, "?") {
		sep = "&"
	}
	dlURL := fmt.Sprintf("%s%sckSize=50", dlEndpoint, sep)

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

				req, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, dlURL, nil)
				if err != nil {
					return
				}
				resp, err := l.client.Do(req)
				if err != nil {
					return
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

	ulEndpoint := s.URL + "/" + s.UlURL
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
				req, err := http.NewRequestWithContext(uploadCtx, http.MethodPost, ulEndpoint, reader)
				if err != nil {
					return
				}
				req.Header.Set("Content-Type", "application/octet-stream")

				resp, err := l.client.Do(req)
				if err != nil {
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				uploadedBytes.Add(int64(payloadSize))
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
		Provider:        "librespeed",
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
