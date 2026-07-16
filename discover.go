package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// discoverFritz finds a Fritz!Box DVB-C channel list on the current LAN.
// Probes, cheapest first: the fritz.box DNS name every box registers, the
// default gateway (boxes are almost always the router), then an SSDP search
// for SAT>IP servers (the DVB-C streamer is one), which also covers mesh
// setups where the TV-serving box is not the gateway.
func discoverFritz() ([]Channel, bool) {
	var candidates []string
	candidates = append(candidates, "http://fritz.box/dvb/m3u/tv.m3u")
	if gw := defaultGateway(); gw != "" {
		candidates = append(candidates, fmt.Sprintf("http://%s/dvb/m3u/tv.m3u", gw))
	}
	if chs, ok := probeCandidates(candidates); ok {
		return chs, true
	}
	var ssdp []string
	for _, host := range ssdpSatIPHosts(1500 * time.Millisecond) {
		ssdp = append(ssdp, fmt.Sprintf("http://%s/dvb/m3u/tv.m3u", host))
	}
	if chs, ok := probeCandidates(ssdp); ok {
		return chs, true
	}
	log.Printf("no Fritz!Box channel list found on this network")
	return nil, false
}

func probeCandidates(urls []string) ([]Channel, bool) {
	for _, u := range urls {
		data, err := quickFetch(u, 3*time.Second) // room for a redirect + TLS handshake
		if err != nil {
			log.Printf("discovery probe %s: %v", u, err)
			continue
		}
		if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("#EXTM3U")) {
			log.Printf("discovery probe %s: not an m3u playlist", u)
			continue
		}
		channels, err := parseM3U(bytes.NewReader(data))
		if err != nil {
			log.Printf("discovery probe %s: %v", u, err)
			continue
		}
		for i := range channels {
			channels[i].Local = true
		}
		log.Printf("discovered Fritz!Box channel list at %s (%d channels)", u, len(channels))
		return channels, true
	}
	return nil, false
}

func quickFetch(u string, timeout time.Duration) ([]byte, error) {
	client := m3uClient(u, timeout)
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
}

// defaultGateway returns the IPv4 default route's gateway address.
func defaultGateway() string {
	out, err := exec.Command("/sbin/route", "-n", "get", "default").Output()
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "gateway:"); ok {
			gw := strings.TrimSpace(v)
			if net.ParseIP(gw) != nil {
				return gw
			}
		}
	}
	return ""
}

// ssdpSatIPHosts multicasts an SSDP M-SEARCH for SAT>IP servers and returns
// the responding hosts.
func ssdpSatIPHosts(timeout time.Duration) []string {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		log.Printf("ssdp: %v", err)
		return nil
	}
	defer conn.Close()

	dst := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	search := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 1\r\n" +
		"ST: urn:ses-com:device:SatIPServer:1\r\n\r\n"
	for i := 0; i < 2; i++ {
		if _, err := conn.WriteTo([]byte(search), dst); err != nil {
			log.Printf("ssdp send: %v", err)
			return nil
		}
	}

	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	seen := map[string]bool{}
	var hosts []string
	buf := make([]byte, 2048)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			break // deadline reached
		}
		if host := ssdpLocationHost(string(buf[:n])); host != "" && !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// ssdpLocationHost pulls the host out of an SSDP response's LOCATION header.
func ssdpLocationHost(resp string) string {
	for _, line := range strings.Split(resp, "\r\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "location") {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(v))
		if err != nil {
			return ""
		}
		return u.Hostname()
	}
	return ""
}
