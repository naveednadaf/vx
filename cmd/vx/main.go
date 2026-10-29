package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"vx/internal/api"
	"vx/internal/app"
	"vx/internal/config"
	"vx/internal/vless"
	vxext "vx/extension"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	if isHelp(cmd) {
		usage()
		return
	}
	if len(args) > 0 && isHelp(args[0]) {
		subUsage(cmd)
		return
	}
	cfgPath := os.Getenv("VX_CONFIG")
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	}

	var err error
	switch cmd {
	case "serve":
		err = cmdServe(cfgPath)
	case "add":
		err = cmdAdd(cfgPath, args)
	case "groups":
		err = cmdGroups(cfgPath)
	case "use":
		err = cmdUse(cfgPath, args)
	case "mode":
		err = cmdMode(cfgPath, args)
	case "use-node":
		err = cmdUseNode(cfgPath, args)
	case "ping":
		err = cmdPing(cfgPath, args)
	case "status":
		err = cmdStatus(cfgPath)
	case "refresh":
		err = cmdRefresh(cfgPath)
	case "stop":
		err = cmdStop(cfgPath)
	case "exec":
		err = cmdExec(cfgPath, args)
	case "ext":
		err = cmdExt(args)
	case "version":
		fmt.Println("vx", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func isHelp(s string) bool {
	switch s {
	case "-h", "--help", "-help", "help":
		return true
	}
	return false
}

func subUsage(cmd string) {
	switch cmd {
	case "serve":
		fmt.Print(`Usage: vx serve

Starts the local xray process and the REST control API (HTTP 127.0.0.1:18082)
in the foreground. Press Ctrl-C to shut down cleanly.

Endpoints opened:
  Control API:   http://127.0.0.1:18082
  HTTP proxy:    http://127.0.0.1:18080
  SOCKS5 proxy:  socks5://127.0.0.1:18081
`)
	case "add":
		fmt.Print(`Usage: vx add <group> [inputs...]

Add nodes to a group. Inputs can be:
  - Single vless:// URLs
  - http:// or https:// subscription links (auto-fetched and base64-decoded)
  - Local file paths (e.g. urls.txt or base64 files)
  - Piped via stdin (cat urls.txt | vx add mygroup)

Examples:
  vx add sg nodes.txt
  vx add sub https://example.com/api/v1/client/subscribe?token=xyz
  cat links.txt | vx add hk
`)
	case "use":
		fmt.Print(`Usage: vx use <group>

Switch the active node group. If the daemon is running, xray is reloaded immediately.
`)
	case "mode":
		fmt.Print(`Usage: vx mode <urltest|balance|direct>

Select routing strategy:
  urltest:  (Lowest ping) Auto-routes through the fastest healthy node
            using xray observatory's 1-minute live health probes.
  balance:  Round-robin distributes connections evenly across healthy nodes.
  direct:   Bypasses proxy (freedom outbound).
`)
	case "use-node":
		fmt.Print(`Usage: vx use-node <index|name>

Pin a specific node by its numerical index (from 'vx ping') or substring of its name.
Switches routing mode to manual.
`)
	case "ping":
		fmt.Print(`Usage: vx ping [group]

Probe TCP connection latency to all nodes in parallel (timeout: 2s).
Highlights the lowest-latency node with "<- lowest".
`)
	case "exec":
		fmt.Print(`Usage: vx exec [--] <command> [args...]

Run a single command with HTTP_PROXY, HTTPS_PROXY, and ALL_PROXY set to the
local proxy (127.0.0.1:18080).

If the vx daemon is NOT running, xray is spawned on-demand for this command
and cleanly stopped when the command exits.

Examples:
  vx exec -- curl -s https://api.ipify.org
  vx exec -- git clone https://github.com/...
  vx exec python3 scraper.py
`)
	case "ext":
		fmt.Print(`Usage: vx ext install [directory]

Extract the embedded Chrome extension to a folder so it can be loaded into Chrome.
Default destination: ~/vx-extension
`)
	case "groups":
		fmt.Print("Usage: vx groups\n\nList all configured groups and their node counts.\n")
	case "status":
		fmt.Print("Usage: vx status\n\nShow daemon running state, active group, routing mode, and currently in-use node.\n")
	case "refresh":
		fmt.Print("Usage: vx refresh\n\nRe-fetch and update all subscription URLs and files across all groups.\n")
	case "stop":
		fmt.Print("Usage: vx stop\n\nSend SIGTERM to the running vx daemon to shut it down cleanly.\n")
	default:
		usage()
	}
}

func usage() {
	fmt.Print(`vx - local xray proxy manager (browser-only / per-request, never system-wide)

Usage: vx <command> [args]

  serve                    Start xray + control API (foreground; Ctrl-C to stop)
  add <group> [inputs...]  Add vless:// links, subscription URLs, or file paths
                           to a group (reads stdin when piped)
  groups                   List groups and node counts
  use <group>              Activate a group (restarts xray)
  mode <m>                 Set mode: urltest | balance | direct
  use-node <idx|name>      Pin a specific node (switches to manual mode)
  ping [group]             Probe node latencies now
  status                   Show daemon/config status
  refresh                  Re-fetch all subscriptions/files
  stop                     Stop the daemon
  exec [--] <cmd> [args]   Run a command with proxy env vars set for that
                           command only (starts xray if needed)
  ext install [dir]        Install the Chrome extension (load unpacked)

Env: VX_CONFIG overrides the config path (default ~/.vx/config.json)
`)
}

// --- daemon <-> CLI transport -------------------------------------------------

func apiBase(cfgPath string) string {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return "http://127.0.0.1:18082"
	}
	return fmt.Sprintf("http://127.0.0.1:%d", cfg.APIPort)
}

func apiCall(cfgPath, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, apiBase(cfgPath)+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 60 * time.Second}
	return client.Do(req)
}

func daemonRunning(cfgPath string) bool {
	resp, err := apiCall(cfgPath, "GET", "/health", nil)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// --- commands -----------------------------------------------------------------

func cmdServe(cfgPath string) error {
	// Single-instance guard, checked BEFORE anything is spawned: if another
	// live daemon exists (via API health, else pidfile), exit immediately so
	// two daemons can never fight over xray.
	if daemonRunning(cfgPath) {
		fmt.Println("vx: daemon already running — nothing to do")
		return nil
	}
	if pid, err := app.ReadPID(cfgPath); err == nil && pid > 0 && pid != os.Getpid() {
		if p, err := os.FindProcess(pid); err == nil {
			if err := p.Signal(syscall.Signal(0)); err == nil {
				fmt.Printf("vx: daemon pid %d appears alive but API is down; killing it and taking over\n", pid)
				_ = p.Signal(syscall.SIGKILL)
				time.Sleep(300 * time.Millisecond)
			}
		}
	}

	a, err := app.Open(cfgPath)
	if err != nil {
		return err
	}
	defer a.Stop()

	if err := a.Apply(true); err != nil {
		fmt.Fprintln(os.Stderr, "warning: xray not started:", err)
		fmt.Fprintln(os.Stderr, "         (control API will still run; add nodes and retry 'vx mode urltest')")
	}
	a.StartLoops()
	if err := app.WritePID(cfgPath, os.Getpid()); err != nil {
		fmt.Fprintln(os.Stderr, "warning: cannot write pidfile:", err)
	}

	srv := api.New(a)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(a.Status().APIPort) }()

	// If another vx daemon already owns the API port, exit immediately so two
	// daemons never fight over xray (the orphaned-xray bug).
	select {
	case err := <-errCh:
		if err != nil && strings.Contains(err.Error(), "address already in use") {
			fmt.Fprintln(os.Stderr, "vx: another daemon already owns the control API — exiting (use 'vx status' or the menu bar app)")
			return nil
		}
	default:
	}

	st := a.Status()
	fmt.Printf("vx %s serving\n", version)
	fmt.Printf("  control API   http://127.0.0.1:%d  (for the Chrome extension)\n", st.APIPort)
	fmt.Printf("  http proxy    http://%s:%d  (browser-only / per-request)\n", st.ProxyHost, st.HTTPPort)
	fmt.Printf("  socks5 proxy  socks5://%s:%d\n", st.ProxyHost, st.SOCKSPort)
	fmt.Printf("  group=%q mode=%s xray=%v\n", st.Group, st.Mode, st.XrayRunning)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		fmt.Println("\nstopping on", sig)
	case err := <-errCh:
		if err != nil && !strings.Contains(err.Error(), "Server closed") {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	_ = os.Remove(app.PIDFilePath(cfgPath))
	return nil
}

func readStdin() string {
	stat, _ := os.Stdin.Stat()
	if stat == nil || stat.Mode()&os.ModeCharDevice != 0 {
		return ""
	}
	data, _ := io.ReadAll(os.Stdin)
	return string(data)
}

func cmdAdd(cfgPath string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: vx add <group> [vless://... | https://sub | file ...]")
	}
	group := args[0]
	inputs := args[1:]

	var srcs []config.Source
	for _, in := range inputs {
		in = strings.TrimSpace(in)
		if in == "" {
			continue
		}
		switch {
		case strings.HasPrefix(in, "vless://"):
			srcs = append(srcs, config.Source{Type: config.SourceVless, Value: in})
		case strings.HasPrefix(in, "http://"), strings.HasPrefix(in, "https://"):
			srcs = append(srcs, config.Source{Type: config.SourceSub, Value: in})
		default:
			abs, err := filepath.Abs(in)
			if err != nil {
				abs = in
			}
			if _, err := os.Stat(abs); err != nil {
				return fmt.Errorf("input %q is not a vless:// link, URL, or existing file", in)
			}
			srcs = append(srcs, config.Source{Type: config.SourceFile, Value: abs})
		}
	}

	// Piped stdin: accept vless links, URLs, or a blob of either.
	if stdinText := readStdin(); stdinText != "" {
		nodes, _ := vless.ParseText(stdinText, "stdin")
		for _, n := range nodes {
			// Reconstruct the original link from params.
			srcs = append(srcs, config.Source{Type: config.SourceVless, Value: nodeToLink(n)})
		}
		if len(nodes) == 0 {
			// Not link lines; maybe a single subscription URL or a base64 blob of links.
			trimmed := strings.TrimSpace(stdinText)
			if vless.IsSubURL(trimmed) && !strings.Contains(trimmed, "\n") {
				srcs = append(srcs, config.Source{Type: config.SourceSub, Value: trimmed})
			} else {
				tmp := filepath.Join(os.TempDir(), fmt.Sprintf("vx-import-%d.txt", time.Now().UnixNano()))
				if err := os.WriteFile(tmp, []byte(stdinText), 0o600); err != nil {
					return err
				}
				defer os.Remove(tmp)
				srcs = append(srcs, config.Source{Type: config.SourceFile, Value: tmp})
			}
		}
	}

	if len(srcs) == 0 {
		return fmt.Errorf("no inputs given (pass links/URLs/files or pipe them on stdin)")
	}

	if daemonRunning(cfgPath) {
		return addViaAPI(cfgPath, group, srcs)
	}

	// Offline path: edit config directly; xray picks it up on next serve.
	a, err := app.Open(cfgPath)
	if err != nil {
		return err
	}
	added, total, warnings, err := a.AddSources(group, srcs, false)
	printWarnings(warnings)
	if err != nil {
		return err
	}
	fmt.Printf("group %q: +%d nodes (total %d)\n", group, added, total)
	fmt.Println("(daemon not running; start with 'vx serve')")
	return nil
}

func nodeToLink(n *config.Node) string {
	var b strings.Builder
	b.WriteString("vless://")
	b.WriteString(n.UUID)
	fmt.Fprintf(&b, "@%s:%d", n.Addr, n.Port)
	if len(n.Params) > 0 {
		b.WriteByte('?')
		first := true
		for _, k := range []string{"type", "encryption", "security", "pbk", "fp", "sni", "sid", "spx", "flow"} {
			if v, ok := n.Params[k]; ok {
				if !first {
					b.WriteByte('&')
				}
				first = false
				b.WriteString(k)
				b.WriteByte('=')
				b.WriteString(v)
			}
		}
	}
	b.WriteString("#")
	b.WriteString(strings.ReplaceAll(n.Name, " ", "%20"))
	return b.String()
}

func addViaAPI(cfgPath, group string, srcs []config.Source) error {
	inputs := make([]string, len(srcs))
	for i, s := range srcs {
		if s.Type == config.SourceFile && strings.HasPrefix(s.Value, os.TempDir()) {
			// Pass temp file content inline so the daemon (same host) can read it.
			data, err := os.ReadFile(s.Value)
			if err != nil {
				return err
			}
			inputs[i] = string(data)
			continue
		}
		inputs[i] = s.Value
	}
	resp, err := apiCall(cfgPath, "POST", "/add", map[string]any{"group": group, "sources": inputs})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK       bool     `json:"ok"`
		Error    string   `json:"error"`
		Added    int      `json:"added"`
		Total    int      `json:"total"`
		Warnings []string `json:"warnings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	printWarnings(out.Warnings)
	if !out.OK {
		return fmt.Errorf("%s", out.Error)
	}
	fmt.Printf("group %q: +%d nodes (total %d)\n", group, out.Added, out.Total)
	return nil
}

func printWarnings(ws []string) {
	for _, w := range ws {
		fmt.Fprintln(os.Stderr, "  warning:", w)
	}
}

func cmdGroups(cfgPath string) error {
	st, err := fetchStatus(cfgPath)
	if err != nil {
		return err
	}
	if len(st.Groups) == 0 {
		fmt.Println("no groups yet; add one: vx add <group> nodes.txt")
		return nil
	}
	for _, g := range st.Groups {
		marker := "  "
		if g.Name == st.Group {
			marker = "* "
		}
		fmt.Printf("%s%-24s %3d nodes  %d sources\n", marker, g.Name, g.NodeCount, len(g.Sources))
	}
	return nil
}

func cmdUse(cfgPath string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: vx use <group>")
	}
	if daemonRunning(cfgPath) {
		resp, err := apiCall(cfgPath, "POST", "/group", map[string]string{"name": args[0]})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return apiError(resp)
		}
		fmt.Printf("active group -> %q\n", args[0])
		return nil
	}
	a, err := app.Open(cfgPath)
	if err != nil {
		return err
	}
	if err := a.SetGroup(args[0]); err != nil {
		return err
	}
	fmt.Printf("active group -> %q (daemon not running; will apply on serve)\n", args[0])
	return nil
}

func cmdMode(cfgPath string, args []string) error {
	if len(args) != 1 || !config.ValidMode(args[0]) {
		return fmt.Errorf("usage: vx mode <urltest|balance|direct>")
	}
	if daemonRunning(cfgPath) {
		resp, err := apiCall(cfgPath, "POST", "/mode", map[string]any{"mode": args[0], "node": -1})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return apiError(resp)
		}
		fmt.Printf("mode -> %s\n", args[0])
		return nil
	}
	a, err := app.Open(cfgPath)
	if err != nil {
		return err
	}
	if err := a.SetMode(args[0], -1); err != nil {
		return err
	}
	fmt.Printf("mode -> %s (daemon not running; will apply on serve)\n", args[0])
	return nil
}

func cmdUseNode(cfgPath string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: vx use-node <index|name>")
	}
	st, err := fetchStatus(cfgPath)
	if err != nil {
		return err
	}
	if st.Group == "" {
		return fmt.Errorf("no active group; run 'vx use <group>' first")
	}
	idx := -1
	if i, err := strconv.Atoi(args[0]); err == nil {
		if i < 0 || i >= len(st.Nodes) {
			return fmt.Errorf("node index %d out of range (0..%d)", i, len(st.Nodes)-1)
		}
		idx = i
	} else {
		for _, n := range st.Nodes {
			if strings.EqualFold(n.Name, args[0]) || strings.Contains(strings.ToLower(n.Name), strings.ToLower(args[0])) {
				idx = n.Index
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("no node matching %q in group %q", args[0], st.Group)
		}
	}
	if daemonRunning(cfgPath) {
		resp, err := apiCall(cfgPath, "POST", "/mode", map[string]any{"mode": config.ModeManual, "node": idx})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return apiError(resp)
		}
	} else {
		a, err := app.Open(cfgPath)
		if err != nil {
			return err
		}
		if err := a.SetMode(config.ModeManual, idx); err != nil {
			return err
		}
	}
	fmt.Printf("pinned node [%d] %s (manual mode)\n", idx, st.Nodes[idx].Name)
	return nil
}

func cmdPing(cfgPath string, args []string) error {
	group := ""
	if len(args) > 0 {
		group = args[0]
	}
	if daemonRunning(cfgPath) {
		path := "/ping"
		if group != "" {
			path += "?group=" + group
		}
		resp, err := apiCall(cfgPath, "POST", path, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return apiError(resp)
		}
		var st statusResp
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			return err
		}
		printNodes(st.Nodes)
		return nil
	}
	// Offline: probe directly from config.
	a, err := app.Open(cfgPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.ForcePing(ctx, group); err != nil {
		return err
	}
	printNodes(a.Status().Nodes)
	return nil
}

func printNodes(nodes []app.NodeStatus) {
	if len(nodes) == 0 {
		fmt.Println("no nodes in active group")
		return
	}
	best := 1 << 30
	for _, n := range nodes {
		if n.PingOK && n.PingMS < best {
			best = n.PingMS
		}
	}
	for _, n := range nodes {
		var s string
		switch {
		case !n.PingOK:
			s = "FAIL  " + n.PingErr
		case n.PingMS == best:
			s = fmt.Sprintf("%4d ms  <- lowest", n.PingMS)
		default:
			s = fmt.Sprintf("%4d ms", n.PingMS)
		}
		fmt.Printf("  [%2d] %-38s %-24s %s\n", n.Index, truncate(n.Name, 38), fmt.Sprintf("%s:%d", n.Addr, n.Port), s)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

type statusResp struct {
	OK          bool              `json:"ok"`
	XrayRunning bool              `json:"xray_running"`
	Mode        string            `json:"mode"`
	Group       string            `json:"group"`
	ManualNode  int               `json:"manual_node"`
	ActiveNode  int               `json:"active_node"`
	HTTPPort    int               `json:"http_port"`
	SOCKSPort   int               `json:"socks_port"`
	APIPort     int               `json:"api_port"`
	Groups      []app.GroupStatus `json:"groups"`
	Nodes       []app.NodeStatus  `json:"nodes"`
	Warnings    []string          `json:"warnings"`
}

func fetchStatus(cfgPath string) (statusResp, error) {
	var st statusResp
	if daemonRunning(cfgPath) {
		resp, err := apiCall(cfgPath, "GET", "/status", nil)
		if err != nil {
			return st, err
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			return st, err
		}
		return st, nil
	}
	ls, err := app.LocalStatus(cfgPath)
	if err != nil {
		return st, err
	}
	b, _ := json.Marshal(ls)
	if err := json.Unmarshal(b, &st); err != nil {
		return st, err
	}
	return st, nil
}

func apiError(resp *http.Response) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if e.Error == "" {
		e.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return fmt.Errorf("%s", e.Error)
}

func cmdStatus(cfgPath string) error {
	st, err := fetchStatus(cfgPath)
	if err != nil {
		return err
	}
	state := "daemon: stopped"
	if st.OK && st.XrayRunning {
		state = fmt.Sprintf("daemon: running (xray pid ok)")
	} else if st.OK {
		state = "daemon: running (xray DOWN)"
	}
	fmt.Printf("vx status — %s\n", state)
	activeLabel := "none"
	if st.ActiveNode >= 0 && st.ActiveNode < len(st.Nodes) {
		activeLabel = fmt.Sprintf("[%d] %s", st.ActiveNode, st.Nodes[st.ActiveNode].Name)
	}
	fmt.Printf("  group=%q mode=%s in_use=%s\n", st.Group, st.Mode, activeLabel)
	fmt.Printf("  http=127.0.0.1:%d socks=127.0.0.1:%d api=127.0.0.1:%d\n", st.HTTPPort, st.SOCKSPort, st.APIPort)
	fmt.Printf("  active nodes: %d\n", len(st.Nodes))
	printWarnings(st.Warnings)
	return nil
}

func cmdRefresh(cfgPath string) error {
	if !daemonRunning(cfgPath) {
		return fmt.Errorf("daemon not running; refresh requires 'vx serve'")
	}
	resp, err := apiCall(cfgPath, "POST", "/refresh", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK       bool     `json:"ok"`
		Warnings []string `json:"warnings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	printWarnings(out.Warnings)
	fmt.Println("refreshed all subscriptions/files")
	return nil
}

func cmdStop(cfgPath string) error {
	pid, err := app.ReadPID(cfgPath)
	if err != nil {
		if !daemonRunning(cfgPath) {
			return fmt.Errorf("daemon not running (no pidfile)")
		}
		return fmt.Errorf("pidfile missing but API is up; stop the 'vx serve' process manually (Ctrl-C)")
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal pid %d: %w", pid, err)
	}
	// Wait for the daemon to clean up xray.
	for i := 0; i < 30; i++ {
		if !daemonRunning(cfgPath) {
			fmt.Printf("daemon (pid %d) stopped\n", pid)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon (pid %d) still responding; try killing it manually", pid)
}

func cmdExec(cfgPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: vx exec <cmd> [args...]  (or: vx exec -- <cmd> ...)")
	}
	args = trimDoubleDash(args)

	owned := false
	if !daemonRunning(cfgPath) {
		a, err := app.Open(cfgPath)
		if err != nil {
			return err
		}
		if err := a.EnsureXray(); err != nil {
			return fmt.Errorf("start xray for exec: %w", err)
		}
		owned = true
		defer a.Stop()
		time.Sleep(300 * time.Millisecond) // let xray settle
		fmt.Fprintln(os.Stderr, "vx: started xray for this command (no daemon running)")
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	proxy := fmt.Sprintf("http://127.0.0.1:%d", cfg.HTTPPort)

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(),
		"HTTP_PROXY="+proxy,
		"HTTPS_PROXY="+proxy,
		"ALL_PROXY="+proxy,
		"http_proxy="+proxy,
		"https_proxy="+proxy,
		"all_proxy="+proxy,
	)
	err = cmd.Run()
	if owned {
		fmt.Fprintln(os.Stderr, "vx: xray stopped")
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}

func trimDoubleDash(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

func cmdExt(args []string) error {
	if len(args) == 0 || args[0] != "install" {
		return fmt.Errorf("usage: vx ext install [dir]")
	}
	dest := filepath.Join(homeDir(), "vx-extension")
	if len(args) > 2 {
		return fmt.Errorf("usage: vx ext install [dir]")
	}
	if len(args) == 2 {
		dest = args[1]
	}
	if err := vxext.Extract(dest); err != nil {
		return err
	}
	fmt.Printf("extension written to %s\n", dest)
	fmt.Println("install in Chrome:")
	fmt.Println("  1. open chrome://extensions")
	fmt.Println("  2. enable 'Developer mode' (top right)")
	fmt.Println("  3. click 'Load unpacked' and pick the folder above")
	return nil
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}
