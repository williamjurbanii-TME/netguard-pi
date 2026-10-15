// Package config loads and validates the NetGuard YAML configuration.
package config

import (
	"fmt"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DataDir          string       `yaml:"data_dir"`
	LogRetentionDays int          `yaml:"log_retention_days"`
	DNS              DNSConfig    `yaml:"dns"`
	Local            LocalConfig  `yaml:"local"`
	DHCP             DHCPConfig   `yaml:"dhcp"`
	Web              WebConfig    `yaml:"web"`
	Blocklists       []ListSource `yaml:"blocklists"`
}

type DNSConfig struct {
	Listen    string   `yaml:"listen"`
	Upstreams []string `yaml:"upstreams"`
	CacheSize int      `yaml:"cache_size"`
	BlockTTL  uint32   `yaml:"block_ttl"`
}

// LocalConfig controls device naming and local name resolution.
type LocalConfig struct {
	// Domain that NetGuard answers for, e.g. "lan" makes emmas-iphone.lan work.
	// Empty disables local names.
	Domain string `yaml:"domain"`
	// DiscoverNames asks devices for their own names (mDNS, NetBIOS) when
	// DHCP didn't provide one.
	DiscoverNames bool `yaml:"discover_names"`
}

type DHCPConfig struct {
	Enabled    bool     `yaml:"enabled"`
	ServerIP   string   `yaml:"server_ip"`
	RangeStart string   `yaml:"range_start"`
	RangeEnd   string   `yaml:"range_end"`
	Netmask    string   `yaml:"netmask"`
	Router     string   `yaml:"router"`
	DNS        []string `yaml:"dns"`
	LeaseHours int      `yaml:"lease_hours"`
	DomainName string   `yaml:"domain_name"`
}

type WebConfig struct {
	Listen   string `yaml:"listen"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type ListSource struct {
	Category string `yaml:"category"`
	Name     string `yaml:"name"`
	URL      string `yaml:"url"`
}

func Default() *Config {
	return &Config{
		DataDir:          "./data",
		LogRetentionDays: 30,
		DNS: DNSConfig{
			Listen:    ":53",
			Upstreams: []string{"1.1.1.1:53", "9.9.9.9:53"},
			CacheSize: 10000,
			BlockTTL:  60,
		},
		Local: LocalConfig{Domain: "lan", DiscoverNames: true},
		DHCP:  DHCPConfig{LeaseHours: 24, Netmask: "255.255.255.0"},
		Web:  WebConfig{Listen: ":8080", Username: "admin"},
	}
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) normalize() error {
	if c.LogRetentionDays <= 0 {
		c.LogRetentionDays = 30
	}
	if c.DNS.CacheSize <= 0 {
		c.DNS.CacheSize = 10000
	}
	if len(c.DNS.Upstreams) == 0 {
		return fmt.Errorf("dns.upstreams must not be empty")
	}
	for i, u := range c.DNS.Upstreams {
		if _, _, err := net.SplitHostPort(u); err != nil {
			c.DNS.Upstreams[i] = net.JoinHostPort(u, "53")
		}
	}
	c.Local.Domain = strings.Trim(strings.ToLower(c.Local.Domain), ".")
	if c.DHCP.DomainName == "" {
		c.DHCP.DomainName = c.Local.Domain
	}
	for _, l := range c.Blocklists {
		if l.Category == "" || l.URL == "" {
			return fmt.Errorf("blocklist %q needs both category and url", l.Name)
		}
	}
	if !c.DHCP.Enabled {
		return nil
	}
	d := &c.DHCP
	for name, v := range map[string]string{
		"server_ip": d.ServerIP, "range_start": d.RangeStart, "range_end": d.RangeEnd, "netmask": d.Netmask,
	} {
		if net.ParseIP(v).To4() == nil {
			return fmt.Errorf("dhcp.%s: %q is not a valid IPv4 address", name, v)
		}
	}
	if d.Router != "" && net.ParseIP(d.Router).To4() == nil {
		return fmt.Errorf("dhcp.router: %q is not a valid IPv4 address", d.Router)
	}
	if len(d.DNS) == 0 {
		d.DNS = []string{d.ServerIP}
	}
	if d.LeaseHours <= 0 {
		d.LeaseHours = 24
	}
	return nil
}
