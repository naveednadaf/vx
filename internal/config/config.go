package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ModeURLTest = "urltest"
	ModeBalance = "balance"
	ModeManual  = "manual"
	ModeDirect  = "direct"
)

const (
	SourceVless = "vless"
	SourceSub   = "sub"
	SourceFile  = "file"
)

type Source struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Node struct {
	Name   string            `json:"name"`
	Addr   string            `json:"addr"`
	Port   int               `json:"port"`
	UUID   string            `json:"uuid"`
	Params map[string]string `json:"params,omitempty"`
	Origin string            `json:"origin,omitempty"`
}

func (n *Node) Key() string {
	return fmt.Sprintf("%s:%d|%s", n.Addr, n.Port, n.UUID)
}

type Group struct {
	Name    string    `json:"name"`
	Sources []Source  `json:"sources"`
	Nodes   []*Node   `json:"nodes"`
	Updated time.Time `json:"updated,omitempty"`
}

type Config struct {
	Mode         string   `json:"mode"`
	ActiveGroup  string   `json:"active_group"`
	ManualNode   int      `json:"manual_node"`
	Groups       []*Group `json:"groups"`
	HTTPPort     int      `json:"http_port"`
	SOCKSPort    int      `json:"socks_port"`
	APIPort      int      `json:"api_port"`
	PingInterval int      `json:"ping_interval_sec"`
	SubInterval  int      `json:"sub_interval_sec"`
}

func Default() *Config {
	return &Config{
		Mode:         ModeURLTest,
		HTTPPort:     18080,
		SOCKSPort:    18081,
		APIPort:      18082,
		PingInterval: 60,
		SubInterval:  3600,
	}
}

func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "vx-config.json"
	}
	return filepath.Join(home, ".vx", "config.json")
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			c := Default()
			if err := c.Save(path); err != nil {
				return nil, err
			}
			return c, nil
		}
		return nil, err
	}
	c := Default()
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, nil
}

func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Config) Group(name string) *Group {
	for _, g := range c.Groups {
		if g.Name == name {
			return g
		}
	}
	return nil
}

func (c *Config) Active() *Group {
	return c.Group(c.ActiveGroup)
}

func (c *Config) AddGroup(name string) *Group {
	if g := c.Group(name); g != nil {
		return g
	}
	g := &Group{Name: name}
	c.Groups = append(c.Groups, g)
	return g
}

func (c *Config) DeleteGroup(name string) bool {
	for i, g := range c.Groups {
		if g.Name == name {
			c.Groups = append(c.Groups[:i], c.Groups[i+1:]...)
			if c.ActiveGroup == name {
				c.ActiveGroup = ""
				c.ManualNode = 0
			}
			return true
		}
	}
	return false
}

func ValidMode(m string) bool {
	switch m {
	case ModeURLTest, ModeBalance, ModeManual, ModeDirect:
		return true
	}
	return false
}

func (c *Config) Validate() error {
	if !ValidMode(c.Mode) {
		return fmt.Errorf("invalid mode %q", c.Mode)
	}
	if c.ActiveGroup != "" && c.Active() == nil {
		return fmt.Errorf("active group %q does not exist", c.ActiveGroup)
	}
	if c.Mode == ModeManual {
		g := c.Active()
		if g == nil {
			return fmt.Errorf("manual mode requires an active group")
		}
		if c.ManualNode < 0 || c.ManualNode >= len(g.Nodes) {
			return fmt.Errorf("manual node index %d out of range (group %q has %d nodes)", c.ManualNode, g.Name, len(g.Nodes))
		}
	}
	return nil
}

func SanitizeName(s string) string {
	s = strings.TrimSpace(s)
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '-'
		}
		return r
	}, s)
}
