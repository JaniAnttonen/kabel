package main

import (
	"log"
	"net"
	"sort"
	"strings"
	"time"
)

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

// watchChannels re-fetches the playlist whenever the network configuration
// changes (e.g. switching from tethering back to the Fritz!Box Wi-Fi), and
// retries every 15s while no channel list has been fetched successfully yet.
// Fresh lists are delivered on the returned channel; wake rouses the GLFW
// event loop after each send.
func watchChannels(url string, fetched bool, wake func()) <-chan []Channel {
	out := make(chan []Channel, 1)
	go func() {
		sig := networkSignature()
		lastTry := time.Now()
		for range time.Tick(3 * time.Second) {
			newSig := networkSignature()
			changed := newSig != sig
			sig = newSig
			if !changed && (fetched || time.Since(lastTry) < 15*time.Second) {
				continue
			}
			if changed {
				log.Printf("network change detected, re-fetching channel list")
				time.Sleep(2 * time.Second) // let DHCP/routes settle
			}
			lastTry = time.Now()
			data, err := fetchM3U(url)
			if err != nil {
				log.Printf("channel list retry: %v", err)
				continue
			}
			channels, err := parseM3U(strings.NewReader(string(data)))
			if err != nil {
				log.Printf("channel list retry: %v", err)
				continue
			}
			cacheM3U(data)
			fetched = true
			// Overwrite any undelivered previous update.
			select {
			case <-out:
			default:
			}
			out <- channels
			wake()
		}
	}()
	return out
}
