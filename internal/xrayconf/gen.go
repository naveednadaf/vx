package xrayconf

import (
	"encoding/json"
	"fmt"
	"os"

	"vx/internal/config"
)

type inbound struct {
	Tag      string          `json:"tag"`
	Listen   string          `json:"listen"`
	Port     int             `json:"port"`
	Protocol string          `json:"protocol"`
	Settings json.RawMessage `json:"settings,omitempty"`
	Sniffing *struct {
		Enabled      bool     `json:"enabled"`
		DestOverride []string `json:"destOverride"`
	} `json:"sniffing,omitempty"`
}

type outbound struct {
	Tag            string          `json:"tag"`
	Protocol       string          `json:"protocol"`
	Settings       json.RawMessage `json:"settings"`
	StreamSettings json.RawMessage `json:"streamSettings,omitempty"`
}

type vlessUser struct {
	ID         string `json:"id"`
	Encryption string `json:"encryption"`
	Flow       string `json:"flow,omitempty"`
}

type vnextServer struct {
	Address string      `json:"address"`
	Port    int         `json:"port"`
	Users   []vlessUser `json:"users"`
}

type vlessSettings struct {
	Vnext []vnextServer `json:"vnext"`
}

type realitySettings struct {
	ServerName  string `json:"serverName,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	PublicKey   string `json:"publicKey,omitempty"`
	ShortID     string `json:"shortId,omitempty"`
	SpiderX     string `json:"spiderX,omitempty"`
}

type streamSettings struct {
	Network         string           `json:"network"`
	Security        string           `json:"security,omitempty"`
	RealitySettings *realitySettings `json:"realitySettings,omitempty"`
	TLSSettings     *realitySettings `json:"tlsSettings,omitempty"`
}

type routingRule struct {
	Type        string   `json:"type"`
	Network     string   `json:"network,omitempty"`
	InboundTag  []string `json:"inboundTag,omitempty"`
	OutboundTag string   `json:"outboundTag,omitempty"`
	BalancerTag string   `json:"balancerTag,omitempty"`
}

type balancer struct {
	Tag      string   `json:"tag"`
	Selector []string `json:"selector"`
	Strategy struct {
		Type     string            `json:"type"`
		Settings *leastLoadSettings `json:"settings,omitempty"`
	} `json:"strategy"`
}

type routing struct {
	Rules     []routingRule `json:"rules"`
	Balancers []balancer    `json:"balancers,omitempty"`
}

type observatory struct {
	SubjectSelector   []string `json:"subjectSelector"`
	ProbeURL          string   `json:"probeUrl"`
	ProbeInterval     string   `json:"probeInterval"`
	EnableConcurrency bool     `json:"enableConcurrency"`
}

type burstObservatory struct {
	SubjectSelector []string `json:"subjectSelector"`
	PingConfig      struct {
		Destination  string `json:"destination"`
		Connectivity string `json:"connectivity"`
		Interval     string `json:"interval"`
		Sampling     int    `json:"sampling"`
		Timeout      string `json:"timeout"`
	} `json:"pingConfig"`
}

type leastLoadSettings struct {
	Baselines []string `json:"baselines"`
	Expected  int      `json:"expected"`
	MaxRTT    string   `json:"maxRTT,omitempty"`
}

type apiConfig struct {
	Tag      string   `json:"tag"`
	Services []string `json:"services"`
}

type configDoc struct {
	Log              json.RawMessage   `json:"log,omitempty"`
	API              *apiConfig        `json:"api,omitempty"`
	Stats            json.RawMessage   `json:"stats,omitempty"`
	Inbounds         []inbound         `json:"inbounds"`
	Outbounds        []outbound        `json:"outbounds"`
	Observatory      *observatory      `json:"observatory,omitempty"`
	BurstObservatory *burstObservatory `json:"burstObservatory,omitempty"`
	Routing          routing           `json:"routing"`
}

// Generate produces an xray config for the given mode and active group.
func Generate(c *config.Config) ([]byte, error) {
	doc := configDoc{}

	sniff := &struct {
		Enabled      bool     `json:"enabled"`
		DestOverride []string `json:"destOverride"`
	}{
		Enabled:      true,
		DestOverride: []string{"http", "tls", "quic"},
	}
	inHTTP := inbound{Tag: "http-in", Listen: "127.0.0.1", Port: c.HTTPPort, Protocol: "http", Sniffing: sniff}
	inSOCKS := inbound{Tag: "socks-in", Listen: "127.0.0.1", Port: c.SOCKSPort, Protocol: "socks", Sniffing: sniff}
	doc.Inbounds = []inbound{inHTTP, inSOCKS}

	// Expose a tiny local-only gRPC API so the daemon can ask the balancer
	// which node is actually serving traffic right now (RouterService).
	apiPort := c.APIPort + 1000
	doc.API = &apiConfig{
		Tag:      "api",
		Services: []string{"RoutingService"},
	}
	doc.Stats = json.RawMessage(`{}`)
	doc.Inbounds = append(doc.Inbounds, inbound{
		Tag:      "api",
		Listen:   "127.0.0.1",
		Port:     apiPort,
		Protocol: "dokodemo-door",
		Settings: json.RawMessage(`{"address":"127.0.0.1"}`),
	})

	doc.Outbounds = append(doc.Outbounds, outbound{
		Tag:      "direct",
		Protocol: "freedom",
		Settings: json.RawMessage(`{}`),
	})

	var nodeTags []string
	g := c.Active()
	if g != nil && len(g.Nodes) > 0 {
		for i, n := range g.Nodes {
			tag := fmt.Sprintf("node-%d", i)
			o, err := nodeOutbound(tag, n)
			if err != nil {
				return nil, fmt.Errorf("node %d (%s): %w", i, n.Name, err)
			}
			doc.Outbounds = append(doc.Outbounds, o)
			nodeTags = append(nodeTags, tag)
		}
	}

	switch c.Mode {
	case config.ModeURLTest:
		if len(nodeTags) == 0 {
			return nil, fmt.Errorf("no nodes in active group")
		}
		doc.Observatory = &observatory{
			SubjectSelector:   []string{"node-"},
			ProbeURL:          "http://www.gstatic.com/generate_204",
			ProbeInterval:     "1m",
			EnableConcurrency: true,
		}
		var b balancer
		b.Tag = "bal"
		b.Selector = []string{"node-"}
		b.Strategy.Type = "leastPing"
		doc.Routing.Balancers = []balancer{b}
		doc.Routing.Rules = []routingRule{
			{Type: "field", InboundTag: []string{"http-in", "socks-in"}, BalancerTag: "bal"},
		}

	case config.ModeBalance:
		if len(nodeTags) == 0 {
			return nil, fmt.Errorf("no nodes in active group")
		}
		// "Balance" = leastLoad: pick from nodes whose ping is stable (low
		// deviation over the sampling window) and near the fastest baseline,
		// rather than blindly round-robining through dead/unstable nodes.
		bo := &burstObservatory{SubjectSelector: []string{"node-"}}
		bo.PingConfig.Destination = "http://www.gstatic.com/generate_204"
		bo.PingConfig.Connectivity = "http://www.gstatic.com/generate_204"
		bo.PingConfig.Interval = "1m"
		bo.PingConfig.Sampling = 3
		bo.PingConfig.Timeout = "3s"
		doc.BurstObservatory = bo
		var b balancer
		b.Tag = "bal"
		b.Selector = []string{"node-"}
		b.Strategy.Type = "leastLoad"
		// baselines: nodes within 200ms of the best are "balanced" candidates;
		// expected 2 keeps 2 fallback nodes. leastLoad inherently ignores
		// high-deviation nodes (cost = stddev penalty in xray).
		b.Strategy.Settings = &leastLoadSettings{
			Baselines: []string{"200ms"},
			Expected:  2,
		}
		doc.Routing.Balancers = []balancer{b}
		doc.Routing.Rules = []routingRule{
			{Type: "field", InboundTag: []string{"http-in", "socks-in"}, BalancerTag: "bal"},
		}

	case config.ModeManual:
		if len(nodeTags) == 0 {
			return nil, fmt.Errorf("no nodes in active group")
		}
		if c.ManualNode < 0 || c.ManualNode >= len(nodeTags) {
			return nil, fmt.Errorf("manual node index %d out of range", c.ManualNode)
		}
		doc.Routing.Rules = []routingRule{
			{Type: "field", InboundTag: []string{"http-in", "socks-in"}, OutboundTag: nodeTags[c.ManualNode]},
		}

	case config.ModeDirect:
		doc.Routing.Rules = []routingRule{
			{Type: "field", InboundTag: []string{"http-in", "socks-in"}, OutboundTag: "direct"},
		}

	default:
		return nil, fmt.Errorf("unknown mode %q", c.Mode)
	}

	// Route the local control API inbound back into xray's own API handler.
	doc.Routing.Rules = append(doc.Routing.Rules, routingRule{
		Type: "field", InboundTag: []string{"api"}, OutboundTag: "api",
	})

	return json.MarshalIndent(doc, "", "  ")
}

func nodeOutbound(tag string, n *config.Node) (outbound, error) {
	encryption := n.Params["encryption"]
	if encryption == "" {
		encryption = "none"
	}
	user := vlessUser{ID: n.UUID, Encryption: encryption, Flow: n.Params["flow"]}
	settings, _ := json.Marshal(vlessSettings{Vnext: []vnextServer{{
		Address: n.Addr,
		Port:    n.Port,
		Users:   []vlessUser{user},
	}}})

	network := n.Params["type"]
	if network == "" {
		network = "tcp"
	}
	security := n.Params["security"]
	ss := streamSettings{Network: network, Security: security}
	switch security {
	case "reality":
		ss.RealitySettings = &realitySettings{
			ServerName:  n.Params["sni"],
			Fingerprint: n.Params["fp"],
			PublicKey:   n.Params["pbk"],
			ShortID:     n.Params["sid"],
			SpiderX:     n.Params["spx"],
		}
	case "tls":
		ss.TLSSettings = &realitySettings{
			ServerName:  n.Params["sni"],
			Fingerprint: n.Params["fp"],
		}
	}
	ssJSON, err := json.Marshal(ss)
	if err != nil {
		return outbound{}, err
	}
	return outbound{Tag: tag, Protocol: "vless", Settings: settings, StreamSettings: ssJSON}, nil
}

// Write atomically writes the config to path.
func Write(path string, doc []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, doc, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
