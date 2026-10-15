// NetGuard: network-wide ad blocking, DHCP and per-device parental controls.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"netguard/internal/clients"
	"netguard/internal/config"
	"netguard/internal/dhcpserver"
	"netguard/internal/dnsserver"
	"netguard/internal/filter"
	"netguard/internal/names"
	"netguard/internal/querylog"
	"netguard/internal/store"
	"netguard/internal/web"
)

// version is set at build time by deploy.ps1 (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	scan := flag.Bool("scan", false, "list devices on the local network with their discovered names, then exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	log.Printf("NetGuard %s starting", version)
	if *scan {
		scanNetwork()
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "netguard.db"))
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	lists := filter.NewListManager(filepath.Join(cfg.DataDir, "lists"), cfg.Blocklists)
	go lists.Refresh(false) // DNS serves unfiltered-by-lists until this finishes

	engine, err := filter.NewEngine(st, lists)
	if err != nil {
		log.Fatalf("engine: %v", err)
	}

	qlog := querylog.New(st)
	logDone := make(chan struct{})
	go func() { qlog.Run(ctx); close(logDone) }()

	var dhcp *dhcpserver.Server
	resolver := clients.NewResolver(nil)
	if cfg.DHCP.Enabled {
		onClient := func(mac, hostname string) {
			id, _ := engine.EnsureDevice(mac, "")
			if hostname != "" {
				engine.SetHostname(id, hostname, "dhcp") // may merge a rotated private MAC
			}
		}
		dhcp, err = dhcpserver.New(cfg.DHCP, st, onClient)
		if err != nil {
			log.Fatalf("dhcp: %v", err)
		}
		engine.OnChange = func() {
			if err := dhcp.LoadReservations(); err != nil {
				log.Printf("reload reservations: %v", err)
			}
		}
		if err := dhcp.Start(); err != nil {
			log.Fatalf("%v", err)
		}
		defer dhcp.Close()
		resolver = clients.NewResolver(dhcp)
	}
	go resolver.Run(ctx)

	var disc *names.Discoverer
	if cfg.Local.DiscoverNames {
		disc = names.NewDiscoverer(engine)
		go disc.Run(ctx)
	}

	dns := dnsserver.New(cfg.DNS, cfg.Local.Domain, engine, resolver, disc, qlog)
	if err := dns.Start(); err != nil {
		log.Fatalf("%v (is another DNS server using the port? try 'netstat -ano | findstr :53')", err)
	}
	defer dns.Shutdown()

	ui := web.New(cfg.Web, version, cfg.Local.Domain, cfg.LogRetentionDays, st, engine, lists, dhcp)
	if err := ui.Start(); err != nil {
		log.Fatalf("web: %v", err)
	}

	go maintenance(ctx, st, lists, time.Duration(cfg.LogRetentionDays)*24*time.Hour)

	<-ctx.Done()
	log.Printf("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ui.Shutdown(shutCtx)
	<-logDone
}

// scanNetwork prints the neighbour table with names discovered via mDNS/NetBIOS.
func scanNetwork() {
	table, err := clients.Neighbors()
	if err != nil {
		log.Fatalf("scan: %v", err)
	}
	ips := make([]string, 0, len(table))
	for ip := range table {
		if v4 := net.ParseIP(ip).To4(); v4 != nil && v4.IsPrivate() {
			ips = append(ips, ip)
		}
	}
	sort.Slice(ips, func(i, j int) bool {
		return binary.BigEndian.Uint32(net.ParseIP(ips[i]).To4()) < binary.BigEndian.Uint32(net.ParseIP(ips[j]).To4())
	})
	disc := names.NewDiscoverer(nil)
	type row struct{ name, src string }
	results := make([]row, len(ips))
	var wg sync.WaitGroup
	for i, ip := range ips {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, src := disc.Lookup(net.ParseIP(ip))
			results[i] = row{n, src}
		}()
	}
	wg.Wait()
	fmt.Printf("%-16s %-18s %-8s %s\n", "IP", "MAC", "SOURCE", "NAME")
	for i, ip := range ips {
		mac := table[ip]
		if names.IsRandomMAC(mac) {
			mac += "*"
		}
		fmt.Printf("%-16s %-18s %-8s %s\n", ip, mac, results[i].src, results[i].name)
	}
	fmt.Println("\n* = private (randomized) Wi-Fi address")
}

func maintenance(ctx context.Context, st *store.Store, lists *filter.ListManager, retention time.Duration) {
	prune := time.NewTicker(time.Hour)
	refresh := time.NewTicker(6 * time.Hour) // Refresh only re-downloads lists older than 24h
	defer prune.Stop()
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-prune.C:
			if n, err := st.PruneQueries(retention); err != nil {
				log.Printf("prune: %v", err)
			} else if n > 0 {
				log.Printf("prune: removed %d old query log entries", n)
			}
		case <-refresh.C:
			lists.Refresh(false)
		}
	}
}
