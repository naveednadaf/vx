package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"vx/internal/config"
	"vx/internal/ping"
	"vx/internal/proc"
	"vx/internal/vless"
	"vx/internal/xrayconf"
)

type App struct {
	mu       sync.Mutex
	cfgPath  string
	dir      string
	cfg      *config.Config
	sup      *proc.Supervisor
	pings    map[string]ping.Result
	// history keeps the last pingHistoryLen samples per node key (one per
	// 60s probe cycle) so balance mode can rank nodes by stability (deviation)
	// without extra probe traffic.
	history  map[string][]int
	lastPing time.Time
	warnings []string
	stopCh   chan struct{}
	stopOnce sync.Once
}

const pingHistoryLen = 3

func Open(cfgPath string) (*App, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	bin, err := proc.FindXray()
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(cfgPath)
	a := &App{
		cfgPath: cfgPath,
		dir:     dir,
		cfg:     cfg,
		sup:     proc.NewSupervisor(bin, filepath.Join(dir, "xray.json"), filepath.Join(dir, "xray.log")),
		pings:   map[string]ping.Result{},
		history: map[string][]int{},
		stopCh:  make(chan struct{}),
	}
	// Migrate legacy file sources out of macOS-protected folders so the
	// periodic refresh never triggers TCC permission prompts.
	migrated := false
	for _, g := range cfg.Groups {
		norm := a.normalizeFileSources(g.Sources)
		if !slicesEqual(norm, g.Sources) {
			g.Sources = norm
			migrated = true
		}
	}
	if migrated {
		_ = cfg.Save(cfgPath)
	}
	return a, nil
}

func slicesEqual(a, b []config.Source) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (a *App) StopCh() <-chan struct{} { return a.stopCh }

func (a *App) Stop() {
	a.stopOnce.Do(func() { close(a.stopCh) })
	a.sup.Stop()
}

// Apply regenerates the xray config and (re)starts xray when it is already
// running or startIfStopped is set.
func (a *App) Apply(startIfStopped bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.applyLocked(startIfStopped)
}

func (a *App) applyLocked(startIfStopped bool) error {
	if err := a.cfg.Validate(); err != nil {
		a.setWarningLocked("config invalid: " + err.Error())
		return err
	}
	doc, err := xrayconf.Generate(a.cfg)
	if err != nil {
		a.setWarningLocked("generate: " + err.Error())
		return err
	}
	if err := xrayconf.Write(a.sup.ConfigPath(), doc); err != nil {
		return fmt.Errorf("write xray config: %w", err)
	}
	running := a.sup.IsRunning()
	if running {
		a.sup.Stop()
		if err := a.sup.Start(); err != nil {
			a.setWarningLocked("xray restart: " + err.Error())
			return err
		}
		a.clearWarningLocked("xray restart")
		return nil
	}
	if startIfStopped {
		if err := a.sup.Start(); err != nil {
			a.setWarningLocked("xray start: " + err.Error())
			return err
		}
		a.clearWarningLocked("xray start")
	}
	return nil
}

// EnsureXray starts xray if it is not running and config is valid.
func (a *App) EnsureXray() error {
	if a.sup.IsRunning() {
		return nil
	}
	return a.Apply(true)
}

func (a *App) SetMode(mode string, node int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !config.ValidMode(mode) {
		return fmt.Errorf("invalid mode %q (want urltest|balance|manual|direct)", mode)
	}
	a.cfg.Mode = mode
	if node >= 0 {
		a.cfg.ManualNode = node
	}
	if err := a.cfg.Validate(); err != nil {
		return err
	}
	if err := a.saveLocked(); err != nil {
		return err
	}
	return a.applyLocked(true)
}

func (a *App) SetGroup(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg.Group(name) == nil {
		return fmt.Errorf("group %q does not exist", name)
	}
	a.cfg.ActiveGroup = name
	if a.cfg.ManualNode >= len(a.cfg.Active().Nodes) {
		a.cfg.ManualNode = 0
	}
	if err := a.saveLocked(); err != nil {
		return err
	}
	return a.applyLocked(true)
}

// normalizeFileSources copies any file source that lives outside the config
// dir into dir/sources/, so the daemon never needs to touch protected macOS
// folders (Desktop, Documents, Downloads) during its periodic refresh loops.
func (a *App) normalizeFileSources(sources []config.Source) []config.Source {
	srcDir := filepath.Join(a.dir, "sources")
	_ = os.MkdirAll(srcDir, 0o755)
	out := make([]config.Source, 0, len(sources))
	for _, s := range sources {
		if s.Type != config.SourceFile {
			out = append(out, s)
			continue
		}
		abs, err := filepath.Abs(s.Value)
		if err != nil {
			out = append(out, s)
			continue
		}
		if abs == a.dir || strings.HasPrefix(abs, a.dir+string(os.PathSeparator)) {
			out = append(out, s)
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			out = append(out, s) // unreadable (TCC denied etc.); leave as-is
			continue
		}
		h := sha256.Sum256([]byte(abs))
		dst := filepath.Join(srcDir, fmt.Sprintf("%x-%s", h[:4], filepath.Base(abs)))
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			out = append(out, s)
			continue
		}
		out = append(out, config.Source{Type: config.SourceFile, Value: dst})
	}
	return out
}

// AddSources resolves the given sources and merges them into a group.
// startIfActive controls whether xray is restarted if the active group is touched.
func (a *App) AddSources(group string, sources []config.Source, startIfActive bool) (added, total int, warnings []string, err error) {
	group = config.SanitizeName(group)
	if group == "" {
		return 0, 0, nil, fmt.Errorf("empty group name")
	}
	sources = a.normalizeFileSources(sources)
	nodes, warnings := vless.ResolveSources(sources)
	if len(nodes) == 0 {
		return 0, 0, warnings, fmt.Errorf("no valid vless links found in the given sources")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	g := a.cfg.AddGroup(group)
	existingSrc := map[string]bool{}
	for _, s := range g.Sources {
		existingSrc[s.Type+"|"+s.Value] = true
	}
	known := map[string]bool{}
	for _, n := range g.Nodes {
		known[n.Key()] = true
	}
	newSrc := 0
	for _, s := range sources {
		k := s.Type + "|" + s.Value
		if !existingSrc[k] {
			g.Sources = append(g.Sources, s)
			existingSrc[k] = true
			newSrc++
		}
	}
	for _, n := range nodes {
		if !known[n.Key()] {
			g.Nodes = append(g.Nodes, n)
			known[n.Key()] = true
			added++
		}
	}
	sortNodes(g.Nodes)
	g.Updated = time.Now()
	if a.cfg.ActiveGroup == "" {
		a.cfg.ActiveGroup = g.Name
	}
	if err := a.saveLocked(); err != nil {
		return added, len(g.Nodes), warnings, err
	}
	restart := startIfActive && (a.cfg.ActiveGroup == g.Name)
	return added, len(g.Nodes), warnings, a.applyLocked(restart)
}

func (a *App) DeleteGroup(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.cfg.DeleteGroup(name) {
		return fmt.Errorf("group %q does not exist", name)
	}
	if err := a.saveLocked(); err != nil {
		return err
	}
	return a.applyLocked(a.sup.IsRunning())
}

// Refresh re-resolves every group's sources (subscriptions, files).
func (a *App) Refresh() []string {
	return a.RefreshGroup("")
}

// RefreshGroup re-resolves sources for a single group (or all groups when
// groupName is empty). Only xray is touched if the active group's nodes
// actually changed — nothing else about the daemon is restarted.
func (a *App) RefreshGroup(groupName string) []string {
	a.mu.Lock()
	var groups []*config.Group
	if groupName == "" {
		groups = make([]*config.Group, len(a.cfg.Groups))
		copy(groups, a.cfg.Groups)
	} else {
		g := a.cfg.Group(groupName)
		if g == nil {
			a.mu.Unlock()
			return []string{fmt.Sprintf("group %q does not exist", groupName)}
		}
		groups = []*config.Group{g}
	}
	a.mu.Unlock()

	var allWarnings []string
	changedActive := false
	for _, g := range groups {
		nodes, warns := vless.ResolveSources(g.Sources)
		for _, w := range warns {
			allWarnings = append(allWarnings, fmt.Sprintf("[%s] %s", g.Name, w))
		}
		a.mu.Lock()
		if len(nodes) > 0 {
			changed := setNodes(g, nodes)
			g.Updated = time.Now()
			if changed && g.Name == a.cfg.ActiveGroup {
				changedActive = true
			}
			if changed {
				_ = a.saveLocked()
			}
		}
		a.mu.Unlock()
	}
	if changedActive {
		if err := a.Apply(a.sup.IsRunning()); err != nil {
			allWarnings = append(allWarnings, err.Error())
		}
	}
	return allWarnings
}

func setNodes(g *config.Group, nodes []*config.Node) bool {
	known := map[string]bool{}
	for _, n := range g.Nodes {
		known[n.Key()] = true
	}
	changed := false
	for _, n := range nodes {
		if !known[n.Key()] {
			g.Nodes = append(g.Nodes, n)
			changed = true
		}
	}
	// Drop nodes that disappeared from their source.
	present := map[string]bool{}
	for _, n := range nodes {
		present[n.Key()] = true
	}
	kept := g.Nodes[:0]
	for _, n := range g.Nodes {
		if present[n.Key()] {
			kept = append(kept, n)
		} else {
			changed = true
		}
	}
	g.Nodes = kept
	if changed {
		sortNodes(g.Nodes)
	}
	return changed
}

// recordHistory appends one sample per node, evicting the oldest once the
// ring reaches pingHistoryLen entries.
func (a *App) recordHistory(res map[string]ping.Result) {
	for k, r := range res {
		if !r.OK {
			continue
		}
		h := a.history[k]
		h = append(h, r.MS)
		if len(h) > pingHistoryLen {
			h = h[len(h)-pingHistoryLen:]
		}
		a.history[k] = h
	}
}

// Stability returns avg and stddev over the node's recent ping samples.
// Nodes with fewer samples than pingHistoryLen report hasFull=false so
// balance ranking can treat them as unproven.
func (a *App) Stability(key string) (avg, stddev float64, hasFull bool) {
	h := a.history[key]
	if len(h) == 0 {
		return 0, 0, false
	}
	sum := 0
	for _, v := range h {
		sum += v
	}
	avg = float64(sum) / float64(len(h))
	if len(h) < 2 {
		return avg, 0, false
	}
	variance := 0.0
	for _, v := range h {
		d := float64(v) - avg
		variance += d * d
	}
	variance /= float64(len(h))
	return avg, math.Sqrt(variance), len(h) >= pingHistoryLen
}

func (a *App) PingOnce(ctx context.Context) {
	a.mu.Lock()
	var nodes []*config.Node
	if g := a.cfg.Active(); g != nil {
		nodes = append(nodes, g.Nodes...)
	}
	a.mu.Unlock()
	if len(nodes) == 0 {
		return
	}
	res := ping.All(ctx, nodes, 30, 2*time.Second)
	a.mu.Lock()
	for k, v := range res {
		a.pings[k] = v
	}
	a.recordHistory(res)
	a.lastPing = time.Now()

	// If the user selected manual mode on a node that has now failed its ping,
	// automatically switch them to the lowest healthy ping node in the group
	// so their internet doesn't freeze.
	if a.cfg.Mode == config.ModeManual {
		g := a.cfg.Active()
		if g != nil && a.cfg.ManualNode >= 0 && a.cfg.ManualNode < len(g.Nodes) {
			currentNode := g.Nodes[a.cfg.ManualNode]
			if curRes, exists := a.pings[currentNode.Key()]; exists && !curRes.OK {
				// Find lowest latency working node
				bestIdx := -1
				bestMS := 999999
				for i, n := range g.Nodes {
					if r, ok := a.pings[n.Key()]; ok && r.OK && r.MS < bestMS {
						bestMS = r.MS
						bestIdx = i
					}
				}
				if bestIdx >= 0 {
					a.cfg.ManualNode = bestIdx
					_ = a.saveLocked()
					_ = a.applyLocked(true)
				}
			}
		}
	}
	a.mu.Unlock()
}

// ForcePing probes synchronously (used by CLI vx ping and API POST /ping).
func (a *App) ForcePing(ctx context.Context, groupName string) error {
	a.mu.Lock()
	var nodes []*config.Node
	if groupName != "" {
		if g := a.cfg.Group(groupName); g != nil {
			nodes = append(nodes, g.Nodes...)
		} else {
			a.mu.Unlock()
			return fmt.Errorf("group %q does not exist", groupName)
		}
	} else if g := a.cfg.Active(); g != nil {
		nodes = append(nodes, g.Nodes...)
	}
	a.mu.Unlock()
	if len(nodes) == 0 {
		return fmt.Errorf("no nodes to ping")
	}
	res := ping.All(ctx, nodes, 30, 2*time.Second)
	a.mu.Lock()
	for k, v := range res {
		a.pings[k] = v
	}
	a.lastPing = time.Now()
	a.mu.Unlock()
	return nil
}

func (a *App) StartLoops() {
	go a.pingLoop()
	go a.refreshLoop()
}

func (a *App) pingLoop() {
	for {
		interval := a.interval(&a.cfg.PingInterval)
		if interval <= 0 {
			interval = 60
		}
		select {
		case <-time.After(time.Duration(interval) * time.Second):
		case <-a.stopCh:
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		a.PingOnce(ctx)
		cancel()
	}
}

func (a *App) refreshLoop() {
	for {
		interval := a.interval(&a.cfg.SubInterval)
		if interval <= 0 {
			interval = 3600
		}
		select {
		case <-time.After(time.Duration(interval) * time.Second):
		case <-a.stopCh:
			return
		}
		a.Refresh()
	}
}

func (a *App) interval(p *int) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return *p
}

func (a *App) saveLocked() error {
	return a.cfg.Save(a.cfgPath)
}

func (a *App) setWarningLocked(w string) {
	for i, x := range a.warnings {
		if x == w {
			a.warnings = append(a.warnings[:i], a.warnings[i+1:]...)
			break
		}
	}
	a.warnings = append(a.warnings, w)
	if len(a.warnings) > 20 {
		a.warnings = a.warnings[len(a.warnings)-20:]
	}
}

func (a *App) clearWarningLocked(prefix string) {
	kept := a.warnings[:0]
	for _, w := range a.warnings {
		if len(w) < len(prefix) || w[:len(prefix)] != prefix {
			kept = append(kept, w)
		}
	}
	a.warnings = kept
}

func sortNodes(nodes []*config.Node) {
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Addr != nodes[j].Addr {
			return nodes[i].Addr < nodes[j].Addr
		}
		return nodes[i].Port < nodes[j].Port
	})
}

func (a *App) Dir() string { return a.dir }

// PingResult exports a cached probe result.
type PingResult = ping.Result

// Snapshot renders the daemon status served to CLI and the extension.
type NodeStatus struct {
	Index   int           `json:"index"`
	Name    string        `json:"name"`
	Addr    string        `json:"addr"`
	Port    int           `json:"port"`
	Key     string        `json:"key"`
	PingMS  int           `json:"ping_ms"`
	PingOK  bool          `json:"ping_ok"`
	PingErr string        `json:"ping_err,omitempty"`
	PingAt  *time.Time    `json:"ping_at,omitempty"`
	// Stability over the last 3 samples (one per 60s probe cycle).
	StabilityAvg int  `json:"stability_avg_ms,omitempty"` // mean of recent samples
	StabilityDev int  `json:"stability_dev_ms,omitempty"` // stddev (deviation)
	Stable       bool `json:"stable,omitempty"`           // full samples && low deviation
}

type GroupStatus struct {
	Name      string     `json:"name"`
	NodeCount int        `json:"node_count"`
	Sources   []config.Source `json:"sources"`
	Updated   *time.Time `json:"updated,omitempty"`
}

type Status struct {
	OK            bool         `json:"ok"`
	XrayRunning   bool         `json:"xray_running"`
	PID           int          `json:"pid,omitempty"`
	Mode          string       `json:"mode"`
	Group         string       `json:"group"`
	ManualNode    int          `json:"manual_node"`
	ActiveNode    int          `json:"active_node"` // index of the node actually in use right now (-1 if unknown/direct)
	ProxyHost     string       `json:"proxy_host"`
	HTTPPort      int          `json:"http_port"`
	SOCKSPort     int          `json:"socks_port"`
	APIPort       int          `json:"api_port"`
	Groups        []GroupStatus `json:"groups"`
	Nodes         []NodeStatus `json:"nodes"`
	LastPingAt    *time.Time   `json:"last_ping_at,omitempty"`
	PingInterval  int          `json:"ping_interval_sec"`
	SubInterval   int          `json:"sub_interval_sec"`
	Warnings      []string     `json:"warnings,omitempty"`
}

func (a *App) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := Status{
		OK:           true,
		XrayRunning:  a.sup.IsRunning(),
		PID:          a.sup.PID(),
		Mode:         a.cfg.Mode,
		Group:        a.cfg.ActiveGroup,
		ManualNode:   a.cfg.ManualNode,
		ActiveNode:   a.detectActiveNodeLocked(),
		ProxyHost:    "127.0.0.1",
		HTTPPort:     a.cfg.HTTPPort,
		SOCKSPort:    a.cfg.SOCKSPort,
		APIPort:      a.cfg.APIPort,
		PingInterval: a.cfg.PingInterval,
		SubInterval:  a.cfg.SubInterval,
		Warnings:     append([]string(nil), a.warnings...),
	}
	if !a.lastPing.IsZero() {
		t := a.lastPing
		st.LastPingAt = &t
	}
	for _, g := range a.cfg.Groups {
		gs := GroupStatus{Name: g.Name, NodeCount: len(g.Nodes), Sources: g.Sources}
		if !g.Updated.IsZero() {
			t := g.Updated
			gs.Updated = &t
		}
		st.Groups = append(st.Groups, gs)
	}
	if g := a.cfg.Active(); g != nil {
		for i, n := range g.Nodes {
			ns := NodeStatus{
				Index: i, Name: n.Name, Addr: n.Addr, Port: n.Port,
				Key: n.Key(), PingMS: -1,
			}
			if r, ok := a.pings[n.Key()]; ok {
				ns.PingOK = r.OK
				ns.PingMS = r.MS
				if !r.OK {
					ns.PingMS = -1
				}
				ns.PingErr = r.Err
				t := r.At
				ns.PingAt = &t
			}
			// Stability metrics from the 3-sample ring (balance-mode ranking data).
			// Stable = full sample set AND deviation below 20% of the average ping.
			if avg, sd, hasFull := a.Stability(n.Key()); len(a.history[n.Key()]) > 0 {
				ns.StabilityAvg = int(avg + 0.5)
				ns.StabilityDev = int(sd + 0.5)
				ns.Stable = hasFull && sd <= 0.20*avg
			}
			st.Nodes = append(st.Nodes, ns)
		}
	}
	return st
}

func (a *App) detectActiveNodeLocked() int {
	if a.cfg.Mode == config.ModeDirect {
		return -1
	}
	if a.cfg.Mode == config.ModeManual {
		return a.cfg.ManualNode
	}
	if !a.sup.IsRunning() {
		return -1
	}
	// Query xray's RoutingService via `xray api bi`
	xrayBin, err := proc.FindXray()
	if err != nil {
		return -1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, xrayBin, "api", "bi", fmt.Sprintf("--server=127.0.0.1:%d", a.cfg.APIPort+1000), "bal")
	out, err := cmd.Output()
	if err != nil {
		return -1
	}
	// Output looks like:
	//   - Selects:
	//     1   node-47
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, "node-"); idx >= 0 {
			numStr := strings.TrimSpace(line[idx+5:])
			// extract leading digits
			end := 0
			for end < len(numStr) && numStr[end] >= '0' && numStr[end] <= '9' {
				end++
			}
			if end > 0 {
				var n int
				if _, err := fmt.Sscan(numStr[:end], &n); err == nil {
					return n
				}
			}
		}
	}
	return -1
}

// LocalStatus reports config state without a running daemon (vx status fallback).
func LocalStatus(cfgPath string) (Status, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return Status{}, err
	}
	st := Status{
		OK:           false,
		Mode:         cfg.Mode,
		Group:        cfg.ActiveGroup,
		ManualNode:   cfg.ManualNode,
		ProxyHost:    "127.0.0.1",
		HTTPPort:     cfg.HTTPPort,
		SOCKSPort:    cfg.SOCKSPort,
		APIPort:      cfg.APIPort,
		PingInterval: cfg.PingInterval,
		SubInterval:  cfg.SubInterval,
	}
	for _, g := range cfg.Groups {
		gs := GroupStatus{Name: g.Name, NodeCount: len(g.Nodes), Sources: g.Sources}
		if !g.Updated.IsZero() {
			t := g.Updated
			gs.Updated = &t
		}
		st.Groups = append(st.Groups, gs)
	}
	return st, nil
}

// PIDFile helpers for `vx stop` without the API.
func PIDFilePath(cfgPath string) string { return filepath.Join(filepath.Dir(cfgPath), "serve.pid") }

func WritePID(cfgPath string, pid int) error {
	return os.WriteFile(PIDFilePath(cfgPath), []byte(fmt.Sprint(pid)), 0o644)
}

func ReadPID(cfgPath string) (int, error) {
	data, err := os.ReadFile(PIDFilePath(cfgPath))
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscan(string(data), &pid); err != nil {
		return 0, err
	}
	return pid, nil
}
