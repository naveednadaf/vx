package xrayconf

import (
	"encoding/json"
	"testing"

	"vx/internal/config"
)

func testConfig(mode string) *config.Config {
	c := config.Default()
	c.Mode = mode
	c.Groups = []*config.Group{{
		Name: "test",
		Nodes: []*config.Node{
			{
				Name: "HK-01", Addr: "1.2.3.4", Port: 443,
				UUID: "d342d11e-d424-4583-b36e-524ab1f0afa4",
				Params: map[string]string{
					"security": "reality", "sni": "example.com", "fp": "firefox",
					"pbk": "Sb3mEpubl1cKEYex4mpleKEYex4mpleKEYexampleKEYexampleKEY",
					"sid": "1a2b3c4d", "spx": "/", "flow": "xtls-rprx-vision",
				},
			},
			{Name: "HK-02", Addr: "5.6.7.8", Port: 443, UUID: "u2", Params: map[string]string{"security": "reality", "sni": "a.com", "fp": "chrome", "pbk": "pk2", "sid": "s2"}},
		},
	}}
	c.ActiveGroup = "test"
	return c
}

func parseDoc(t *testing.T, c *config.Config) map[string]any {
	t.Helper()
	raw, err := Generate(c)
	if err != nil {
		t.Fatalf("generate(%s): %v", c.Mode, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	return doc
}

func TestGenerateModes(t *testing.T) {
	for _, mode := range []string{config.ModeURLTest, config.ModeBalance, config.ModeManual, config.ModeDirect} {
		c := testConfig(mode)
		doc := parseDoc(t, c)
		outs, _ := doc["outbounds"].([]any)
		if len(outs) < 3 {
			t.Errorf("%s: expected >=3 outbounds, got %d", mode, len(outs))
		}
		routing, _ := doc["routing"].(map[string]any)
		rules, _ := routing["rules"].([]any)
		if len(rules) == 0 {
			t.Errorf("%s: no routing rules", mode)
		}
	}
}

func TestURLTestObservatoryPresent(t *testing.T) {
	c := testConfig(config.ModeURLTest)
	doc := parseDoc(t, c)
	obs, ok := doc["observatory"].(map[string]any)
	if !ok || obs == nil {
		t.Fatal("observatory missing")
	}
	if obs["probeUrl"] != "http://www.gstatic.com/generate_204" {
		t.Errorf("probeUrl = %v", obs["probeUrl"])
	}
	routing, _ := doc["routing"].(map[string]any)
	bals, _ := routing["balancers"].([]any)
	if len(bals) != 1 {
		t.Fatalf("expected 1 balancer, got %d", len(bals))
	}
	b := bals[0].(map[string]any)
	strat := b["strategy"].(map[string]any)
	if strat["type"] != "leastPing" {
		t.Errorf("expected leastPing strategy, got %v", strat["type"])
	}
}

func TestBalanceLeastLoad(t *testing.T) {
	c := testConfig(config.ModeBalance)
	doc := parseDoc(t, c)
	routing, _ := doc["routing"].(map[string]any)
	bals, _ := routing["balancers"].([]any)
	if len(bals) != 1 {
		t.Fatalf("expected 1 balancer, got %d", len(bals))
	}
	b := bals[0].(map[string]any)
	strat := b["strategy"].(map[string]any)
	if strat["type"] != "leastLoad" {
		t.Errorf("strategy = %v, want leastLoad", strat["type"])
	}
	settings, _ := strat["settings"].(map[string]any)
	if settings == nil {
		t.Fatal("leastLoad settings missing")
	}
	baselines, _ := settings["baselines"].([]any)
	if len(baselines) == 0 || baselines[0] != "200ms" {
		t.Errorf("baselines = %v", baselines)
	}
	// burstObservatory must be present for leastLoad health data
	if _, ok := doc["burstObservatory"]; !ok {
		t.Error("burstObservatory missing for balance mode")
	}
}

func TestManualPicksNode(t *testing.T) {
	c := testConfig(config.ModeManual)
	c.ManualNode = 1
	doc := parseDoc(t, c)
	routing, _ := doc["routing"].(map[string]any)
	rules := routing["rules"].([]any)
	r0 := rules[0].(map[string]any)
	if r0["outboundTag"] != "node-1" {
		t.Errorf("outboundTag = %v", r0["outboundTag"])
	}
}

func TestManualOutOfRange(t *testing.T) {
	c := testConfig(config.ModeManual)
	c.ManualNode = 5
	if _, err := Generate(c); err == nil {
		t.Error("expected error for out-of-range manual node")
	}
}

func TestLoopbackOnly(t *testing.T) {
	c := testConfig(config.ModeURLTest)
	raw, _ := Generate(c)
	var doc struct {
		Inbounds []struct {
			Listen string `json:"listen"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, in := range doc.Inbounds {
		if in.Listen != "127.0.0.1" {
			t.Errorf("inbound listens on %q, want 127.0.0.1", in.Listen)
		}
	}
}
