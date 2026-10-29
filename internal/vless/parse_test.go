package vless

import (
	"strings"
	"testing"

	"vx/internal/config"
)

const sampleLine = "vless://d342d11e-d424-4583-b36e-524ab1f0afa4@203.0.113.10:443?type=tcp&encryption=none&security=reality&pbk=SOmE publ icKE Yexa mpleK EYexa mpleKEYexampleKEYexampleKEY=&fp=firefox&sni=example.com&sid=1a2b3c4d&spx=%2F&flow=xtls-rprx-vision#Example-HK-01"

func TestParseLine(t *testing.T) {
	n, err := ParseLine(sampleLine, "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if n.Name != "Example-HK-01" {
		t.Errorf("name = %q", n.Name)
	}
	if n.Addr != "203.0.113.10" || n.Port != 443 {
		t.Errorf("addr = %s:%d", n.Addr, n.Port)
	}
	if n.UUID != "d342d11e-d424-4583-b36e-524ab1f0afa4" {
		t.Errorf("uuid = %q", n.UUID)
	}
	if n.Params["security"] != "reality" || n.Params["sni"] != "example.com" {
		t.Errorf("params = %v", n.Params)
	}
	if n.Params["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow = %q", n.Params["flow"])
	}
	if n.Params["spx"] != "/" {
		t.Errorf("spx decoded = %q", n.Params["spx"])
	}
}

func TestParseLineRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "http://example.com", "vless://no-host", "vmess://abc"} {
		if _, err := ParseLine(bad, "test"); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestSplitLinksHandlesMixedText(t *testing.T) {
	text := "junk line\n" + sampleLine + "\n\nnot-a-url\n"
	nodes, skipped := ParseText(text, "t")
	if len(nodes) != 1 || skipped != 2 {
		t.Errorf("got %d nodes, %d skipped", len(nodes), skipped)
	}
}

func TestSplitLinksHandlesBase64Blob(t *testing.T) {
	blob := "dmxlc3M6Ly9kMzQyZDExZS1kNDI0LTQ1ODMtYjM2ZS01MjRhYjFmMGFmYTRAMjAzLjAuMTEzLjEwOjQ0MyN0ZXN0"
	// decode -> vless://d342d11e-d424-4583-b36e-524ab1f0afa4@203.0.113.10:443#test (203.0.113.x = TEST-NET-3 doc range)
	dec, ok := DecodeBase64(blob)
	if !ok || !strings.Contains(dec, "vless://") {
		t.Fatal("base64 decode failed")
	}
	nodes, _ := ParseText(blob, "t")
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node from base64 blob, got %d", len(nodes))
	}
}

func TestDecodeBase64RejectsPlain(t *testing.T) {
	if _, ok := DecodeBase64("not base64 !!!"); ok {
		t.Error("should not decode garbage")
	}
}

func TestResolveSourcesDedupes(t *testing.T) {
	sources := []config.Source{
		{Type: config.SourceVless, Value: sampleLine},
		{Type: config.SourceVless, Value: sampleLine},
	}
	nodes, _ := ResolveSources(sources)
	if len(nodes) != 1 {
		t.Errorf("expected dedupe to 1 node, got %d", len(nodes))
	}
}
