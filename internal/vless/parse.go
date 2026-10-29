package vless

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"vx/internal/config"
)

func IsSubURL(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func ParseLine(line, origin string) (*config.Node, error) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "vless://") {
		return nil, fmt.Errorf("not a vless:// link")
	}
	u, err := url.Parse(line)
	if err != nil {
		return nil, fmt.Errorf("parse link: %w", err)
	}
	uuid := u.User.Username()
	if uuid == "" {
		return nil, fmt.Errorf("missing uuid")
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("missing host")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port %q", u.Port())
	}
	params := map[string]string{}
	for k, vs := range u.Query() {
		if len(vs) > 0 && vs[0] != "" {
			params[k] = vs[0]
		}
	}
	name := strings.TrimSpace(u.Fragment)
	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}
	return &config.Node{
		Name:   name,
		Addr:   host,
		Port:   port,
		UUID:   uuid,
		Params: params,
		Origin: origin,
	}, nil
}

// ParseText extracts vless:// links from arbitrary text (plain lines or base64 blob).
// Non-empty lines that do not yield a valid link are counted as skipped.
func ParseText(text, origin string) (nodes []*config.Node, skipped int) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, 0
	}
	if !strings.Contains(text, "vless://") {
		if dec, ok := DecodeBase64(text); ok && strings.Contains(dec, "vless://") {
			text = dec
		} else {
			return nil, 0
		}
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		link, found := strings.CutPrefix(line, "vless://")
		if !found {
			if idx := strings.Index(line, "vless://"); idx >= 0 {
				link, _ = strings.CutPrefix(line[idx:], "vless://")
			} else {
				skipped++
				continue
			}
		}
		n, err := ParseLine("vless://"+link, origin)
		if err != nil || seen[n.Key()] {
			skipped++
			continue
		}
		seen[n.Key()] = true
		nodes = append(nodes, n)
	}
	return nodes, skipped
}

func DecodeBase64(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), "")
	if s == "" {
		return "", false
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil && len(b) > 0 {
			return string(b), true
		}
	}
	return "", false
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

func FetchSub(subURL string) (nodes []*config.Node, skipped int, err error) {
	req, err := http.NewRequest(http.MethodGet, subURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "v2rayN/6.23")
	req.Header.Set("Accept", "*/*")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("subscription returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, err
	}
	nodes, skipped = ParseText(string(body), subURL)
	if len(nodes) == 0 {
		return nil, skipped, fmt.Errorf("no vless:// links found in subscription")
	}
	return nodes, skipped, nil
}

// ResolveSources resolves a group's sources into nodes. file sources are re-read,
// sub sources are re-fetched, vless sources are parsed as-is.
func ResolveSources(sources []config.Source) (nodes []*config.Node, warnings []string) {
	seen := map[string]bool{}
	add := func(n *config.Node) {
		k := n.Key()
		if !seen[k] {
			seen[k] = true
			nodes = append(nodes, n)
		}
	}
	for _, src := range sources {
		switch src.Type {
		case config.SourceVless:
			if n, err := ParseLine(src.Value, src.Value); err == nil {
				add(n)
			}
		case config.SourceSub:
			ns, skipped, err := FetchSub(src.Value)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("sub %s: %v", src.Value, err))
				continue
			}
			if skipped > 0 {
				warnings = append(warnings, fmt.Sprintf("sub %s: skipped %d non-vless entries", src.Value, skipped))
			}
			for _, n := range ns {
				add(n)
			}
		case config.SourceFile:
			data, err := readFileOrDecode(src.Value)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("file %s: %v", src.Value, err))
				continue
			}
			ns, skipped := ParseText(data, src.Value)
			if skipped > 0 {
				warnings = append(warnings, fmt.Sprintf("file %s: skipped %d bad lines", src.Value, skipped))
			}
			for _, n := range ns {
				add(n)
			}
		}
	}
	return nodes, warnings
}
