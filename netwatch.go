package main

import (
	"log"
	"net"
	"sort"
	"strings"
	"time"
)

// sourceUpdate carries refreshed channel lists; a nil public slice means
// "unchanged", and localSet distinguishes "no update" from "local source
// went away".
type sourceUpdate struct {
	public   []Channel
	local    []Channel
	localSet bool
}

// networkSignature fingerprints the host's current IP configuration so we
// can notice Wi-Fi/tethering switches without any macOS-specific APIs.
func networkSignature() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "unknown"
	}
	ips := make([]string, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.String())
	}
	sort.Strings(ips)
	return strings.Join(ips, ",")
}

// watchSources keeps both channel sources fresh: it discovers a Fritz!Box
// on startup and after every network change (dropping the local section
// when none is found anymore), re-fetches the public playlist on network
// changes, and retries every 15s while it has never loaded. discover=false
// disables the Fritz!Box side (explicit -url). Updates are delivered on the
// returned channel; wake rouses the GLFW event loop after each send.
func watchSources(publicURL string, havePublic, discover bool, wake func()) <-chan sourceUpdate {
	out := make(chan sourceUpdate, 8)
	send := func(u sourceUpdate) {
		select {
		case out <- u:
			wake()
		default: // main loop is far behind; drop rather than block
		}
	}
	fetchPublic := func() bool {
		data, err := fetchM3U(publicURL)
		if err != nil {
			log.Printf("public channel list: %v", err)
			return false
		}
		channels, err := parseM3U(strings.NewReader(string(data)))
		if err != nil {
			log.Printf("public channel list: %v", err)
			return false
		}
		cacheM3U(data)
		send(sourceUpdate{public: channels})
		return true
	}
	haveLocal := false
	runDiscovery := func() {
		if !discover {
			return
		}
		if local, ok := discoverFritz(); ok {
			haveLocal = true
			send(sourceUpdate{local: local, localSet: true})
		} else if haveLocal {
			haveLocal = false
			send(sourceUpdate{localSet: true}) // box went away; drop stale section
		}
	}

	go func() {
		runDiscovery()
		sig := networkSignature()
		lastTry := time.Now()
		lastDisc := time.Now()
		for range time.Tick(3 * time.Second) {
			newSig := networkSignature()
			changed := newSig != sig
			sig = newSig
			if changed {
				log.Printf("network change detected, refreshing channel sources")
				time.Sleep(2 * time.Second) // let DHCP/routes settle
			}
			if changed || (!havePublic && time.Since(lastTry) > 15*time.Second) {
				lastTry = time.Now()
				if fetchPublic() {
					havePublic = true
				}
			}
			// Retry discovery while nothing was found: covers a Fritz!Box
			// booting up and the Local Network permission being granted
			// after launch.
			if changed || (!haveLocal && time.Since(lastDisc) > 30*time.Second) {
				lastDisc = time.Now()
				runDiscovery()
			}
		}
	}()
	return out
}
