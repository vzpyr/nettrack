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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type OoklaServerItem struct {
	ID       interface{} `json:"id"`
	Name     string      `json:"name"`
	Country  string      `json:"country"`
	CC       string      `json:"cc"`
	Sponsor  string      `json:"sponsor"`
	Host     string      `json:"host"`
	URL      string      `json:"url"`
	Lat      interface{} `json:"lat"`
	Lon      interface{} `json:"lon"`
	Distance interface{} `json:"distance"`
}

type OoklaProvider struct {
	client *http.Client
}

func NewOoklaProvider() *OoklaProvider {
	return &OoklaProvider{
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

func (o *OoklaProvider) Type() string {
	return "ookla"
}

func (o *OoklaProvider) ListServers(ctx context.Context) ([]Server, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.speedtest.net/api/js/servers?engine=js&https_functional=true&limit=50", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status from ookla servers list: %d", resp.StatusCode)
	}

	var rawServers []OoklaServerItem
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

		serverURL := item.URL
		if strings.HasPrefix(serverURL, "http://") {
			serverURL = "https://" + strings.TrimPrefix(serverURL, "http://")
		}

		displayName := fmt.Sprintf("%s, %s (%s)", item.Name, item.Country, item.Sponsor)
		if idStr != "" {
			displayName = fmt.Sprintf("%s, %s (%s) [%s]", item.Name, item.Country, item.Sponsor, idStr)
		}

		servers = append(servers, Server{
			ID:      idStr,
			Type:    "ookla",
			Name:    displayName,
			Sponsor: item.Sponsor,
			Country: item.Country,
			Host:    item.Host,
			URL:     serverURL,
		})
	}

	return servers, nil
}

func (o *OoklaProvider) Run(ctx context.Context, s Server, onProgress func(Progress)) (*Result, error) {
	startTime := time.Now()

	onProgress(Progress{
		Stage:    "ping",
		Progress: 0.05,
	})

	baseURL := s.URL
	if idx := strings.LastIndex(baseURL, "/"); idx != -1 {
		baseURL = baseURL[:idx]
	}

	pingURL := baseURL + "/latency.txt"
	pingSamples := make([]float64, 0, 10)

	for i := 0; i < 10; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		reqStart := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pingURL, nil)
		if err != nil {
			req, _ = http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
		}
		resp, err := o.client.Do(req)
		if err != nil {
			req2, err2 := http.NewRequestWithContext(ctx, http.MethodGet, strings.Replace(pingURL, "https://", "http://", 1), nil)
			if err2 == nil {
				resp, err = o.client.Do(req2)
			}
		}
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

	dlURL := baseURL + "/random4000x4000.jpg"

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
				resp, err := o.client.Do(req)
				if err != nil {
					req2, err2 := http.NewRequestWithContext(downloadCtx, http.MethodGet, strings.Replace(dlURL, "https://", "http://", 1), nil)
					if err2 != nil {
						return
					}
					resp, err = o.client.Do(req2)
					if err != nil {
						return
					}
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

	ulEndpoint := s.URL
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
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

				resp, err := o.client.Do(req)
				if err != nil {
					req2, err2 := http.NewRequestWithContext(uploadCtx, http.MethodPost, strings.Replace(ulEndpoint, "https://", "http://", 1), bytes.NewReader(randomPayload))
					if err2 != nil {
						return
					}
					resp, err = o.client.Do(req2)
					if err != nil {
						return
					}
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
		Provider:        "ookla",
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
