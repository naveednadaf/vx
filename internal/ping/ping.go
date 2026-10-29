package ping

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"vx/internal/config"
)

type Result struct {
	Key   string    `json:"key"`
	OK    bool      `json:"ok"`
	MS    int       `json:"ms"`
	Err   string    `json:"err,omitempty"`
	At    time.Time `json:"at"`
}

const defaultTimeout = 2 * time.Second

// Probe measures TCP connect latency to a node.
func Probe(ctx context.Context, addr string, port int, timeout time.Duration) Result {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	r := Result{Key: fmt.Sprintf("%s:%d", addr, port), At: time.Now()}
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(addr, fmt.Sprint(port)))
	if err != nil {
		r.Err = err.Error()
		return r
	}
	r.MS = int(time.Since(start).Milliseconds())
	r.OK = true
	conn.Close()
	return r
}

// All probes nodes in parallel and returns results keyed by node.Key().
func All(ctx context.Context, nodes []*config.Node, workers int, timeout time.Duration) map[string]Result {
	if workers <= 0 {
		workers = 20
	}
	out := make(map[string]Result, len(nodes))
	var mu sync.Mutex
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, n := range nodes {
		n := n
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res := Probe(ctx, n.Addr, n.Port, timeout)
			mu.Lock()
			out[n.Key()] = res
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// SortedKeys returns node keys ordered by ascending latency (failures last).
func SortedKeys(res map[string]Result) []string {
	keys := make([]string, 0, len(res))
	for k := range res {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := res[keys[i]], res[keys[j]]
		if a.OK != b.OK {
			return a.OK
		}
		if a.OK && b.OK {
			return a.MS < b.MS
		}
		return a.Key < b.Key
	})
	return keys
}
