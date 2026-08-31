package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	ErrTestAlreadyRunning = errors.New("a speedtest is already in progress")
	ErrProviderNotFound   = errors.New("provider not found")
	ErrServerNotFound     = errors.New("server not found")
)

type Server struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Name     string  `json:"name"`
	Sponsor  string  `json:"sponsor"`
	Country  string  `json:"country"`
	Host     string  `json:"host"`
	URL      string  `json:"url"`
	PingURL  string  `json:"ping_url,omitempty"`
	DlURL    string  `json:"dl_url,omitempty"`
	UlURL    string  `json:"ul_url,omitempty"`
	Lat      float64 `json:"lat,omitempty"`
	Lon      float64 `json:"lon,omitempty"`
	Distance float64 `json:"distance,omitempty"`
}

type Progress struct {
	ID         string  `json:"id,omitempty"`
	Stage      string  `json:"stage"`
	Progress   float64 `json:"progress"`
	Ping       float64 `json:"ping"`
	Jitter     float64 `json:"jitter"`
	Download   float64 `json:"download"`
	Upload     float64 `json:"upload"`
	BytesDone  int64   `json:"bytes_done"`
	Duration   float64 `json:"duration"`
	ServerName string  `json:"server_name,omitempty"`
	ServerType string  `json:"server_type,omitempty"`
	Error      string  `json:"error,omitempty"`
}

type Result struct {
	ID              string  `json:"id"`
	Timestamp       int64   `json:"timestamp"`
	Provider        string  `json:"provider"`
	ServerID        string  `json:"server_id"`
	ServerName      string  `json:"server_name"`
	ServerCountry   string  `json:"server_country"`
	ServerSponsor   string  `json:"server_sponsor"`
	ServerHost      string  `json:"server_host"`
	DownloadMbps    float64 `json:"download_mbps"`
	UploadMbps      float64 `json:"upload_mbps"`
	PingMs          float64 `json:"ping_ms"`
	JitterMs        float64 `json:"jitter_ms"`
	DurationS       float64 `json:"duration_s"`
	BytesDownloaded int64   `json:"bytes_downloaded"`
	BytesUploaded   int64   `json:"bytes_uploaded"`
	IsScheduled     bool    `json:"is_scheduled"`
	Status          string  `json:"status"`
	Error           string  `json:"error,omitempty"`
	CreatedAt       int64   `json:"created_at"`
}

type Provider interface {
	Type() string
	ListServers(ctx context.Context) ([]Server, error)
	Run(ctx context.Context, server Server, onProgress func(Progress)) (*Result, error)
}

type Manager struct {
	mu           sync.RWMutex
	runMu        sync.Mutex
	providers    map[string]Provider
	currentProg  Progress
	cancelActive context.CancelFunc
	listeners    map[chan Progress]struct{}
}

func NewManager() *Manager {
	return &Manager{
		providers: make(map[string]Provider),
		listeners: make(map[chan Progress]struct{}),
		currentProg: Progress{
			Stage: "idle",
		},
	}
}

func (m *Manager) Register(p Provider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers[p.Type()] = p
}

func (m *Manager) GetProvider(providerType string) (Provider, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.providers[providerType]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotFound, providerType)
	}
	return p, nil
}

func (m *Manager) ListServers(ctx context.Context, providerType string) ([]Server, error) {
	p, err := m.GetProvider(providerType)
	if err != nil {
		return nil, err
	}
	return p.ListServers(ctx)
}

func (m *Manager) GetStatus() Progress {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentProg
}

func (m *Manager) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentProg.Stage != "idle" && m.currentProg.Stage != "complete" && m.currentProg.Stage != "error"
}

func (m *Manager) Subscribe() (chan Progress, func()) {
	ch := make(chan Progress, 16)
	m.mu.Lock()
	m.listeners[ch] = struct{}{}
	current := m.currentProg
	if current.Stage != "ping" && current.Stage != "download" && current.Stage != "upload" {
		current = Progress{Stage: "idle"}
	}
	m.mu.Unlock()

	ch <- current

	unsubscribe := func() {
		m.mu.Lock()
		delete(m.listeners, ch)
		close(ch)
		m.mu.Unlock()
	}
	return ch, unsubscribe
}

func (m *Manager) broadcast(p Progress) {
	m.mu.Lock()
	m.currentProg = p
	for ch := range m.listeners {
		select {
		case ch <- p:
		default:
		}
	}
	m.mu.Unlock()
}

func (m *Manager) CancelActive() {
	m.mu.Lock()
	if m.cancelActive != nil {
		m.cancelActive()
	}
	m.mu.Unlock()
}

func (m *Manager) RunTest(parentCtx context.Context, providerType string, serverID string, isScheduled bool) (*Result, error) {
	if !m.runMu.TryLock() {
		return nil, ErrTestAlreadyRunning
	}
	defer m.runMu.Unlock()

	p, err := m.GetProvider(providerType)
	if err != nil {
		return nil, err
	}

	servers, err := p.ListServers(parentCtx)
	if err != nil {
		return nil, err
	}

	var selectedServer Server
	if serverID == "" || serverID == "auto" {
		if len(servers) == 0 {
			return nil, ErrServerNotFound
		}
		selectedServer = servers[0]
	} else {
		found := false
		for _, s := range servers {
			if s.ID == serverID {
				selectedServer = s
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: %s", ErrServerNotFound, serverID)
		}
	}

	testID := uuid.New().String()
	ctx, cancel := context.WithCancel(parentCtx)
	m.mu.Lock()
	m.cancelActive = cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.cancelActive = nil
		m.mu.Unlock()
	}()

	m.broadcast(Progress{
		ID:         testID,
		Stage:      "ping",
		Progress:   0,
		ServerName: selectedServer.Name,
		ServerType: selectedServer.Type,
	})

	startTime := time.Now()
	res, err := p.Run(ctx, selectedServer, func(prog Progress) {
		prog.ID = testID
		prog.ServerName = selectedServer.Name
		prog.ServerType = selectedServer.Type
		if prog.Duration == 0 {
			prog.Duration = math.Round(time.Since(startTime).Seconds()*10) / 10
		}
		m.broadcast(prog)
	})

	if err != nil {
		m.broadcast(Progress{
			ID:         testID,
			Stage:      "error",
			Progress:   1.0,
			ServerName: selectedServer.Name,
			ServerType: selectedServer.Type,
			Error:      err.Error(),
			Duration:   math.Round(time.Since(startTime).Seconds()*10) / 10,
		})
		go func() {
			time.Sleep(3 * time.Second)
			m.mu.Lock()
			if m.currentProg.ID == testID && m.currentProg.Stage == "error" {
				m.currentProg = Progress{Stage: "idle"}
				for ch := range m.listeners {
					select {
					case ch <- Progress{Stage: "idle"}:
					default:
					}
				}
			}
			m.mu.Unlock()
		}()
		failedRes := &Result{
			ID:            testID,
			Timestamp:     time.Now().Unix(),
			Provider:      providerType,
			ServerID:      selectedServer.ID,
			ServerName:    selectedServer.Name,
			ServerCountry: selectedServer.Country,
			ServerSponsor: selectedServer.Sponsor,
			ServerHost:    selectedServer.Host,
			Status:        "failed",
			Error:         err.Error(),
			DurationS:     math.Round(time.Since(startTime).Seconds()*10) / 10,
			IsScheduled:   isScheduled,
			CreatedAt:     time.Now().Unix(),
		}
		return failedRes, err
	}

	res.ID = testID
	res.IsScheduled = isScheduled
	if res.Status == "" {
		res.Status = "success"
	}
	if res.Timestamp == 0 {
		res.Timestamp = time.Now().Unix()
	}
	if res.CreatedAt == 0 {
		res.CreatedAt = res.Timestamp
	}

	m.broadcast(Progress{
		ID:         testID,
		Stage:      "complete",
		Progress:   1.0,
		Ping:       res.PingMs,
		Jitter:     res.JitterMs,
		Download:   res.DownloadMbps,
		Upload:     res.UploadMbps,
		ServerName: res.ServerName,
		ServerType: res.Provider,
		Duration:   res.DurationS,
	})

	go func() {
		time.Sleep(3 * time.Second)
		m.mu.Lock()
		if m.currentProg.ID == testID && (m.currentProg.Stage == "complete" || m.currentProg.Stage == "error") {
			m.currentProg = Progress{Stage: "idle"}
			for ch := range m.listeners {
				select {
				case ch <- Progress{Stage: "idle"}:
				default:
				}
			}
		}
		m.mu.Unlock()
	}()

	return res, nil
}
