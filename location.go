package main

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// homeCountry guesses the user's country (ISO 3166 alpha-2) without any
// location permissions: the system time zone maps to a country via the
// zoneinfo database, with the POSIX locale as fallback. Empty if unknown.
func homeCountry() string {
	if zone := localZoneName(); zone != "" {
		if f, err := os.Open("/usr/share/zoneinfo/zone.tab"); err == nil {
			defer f.Close()
			if cc := zoneTabCountry(f, zone); cc != "" {
				return cc
			}
		}
	}
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		v := os.Getenv(key) // e.g. fi_FI.UTF-8; usually unset for GUI apps
		if i := strings.Index(v, "_"); i >= 0 && len(v) >= i+3 {
			return strings.ToUpper(v[i+1 : i+3])
		}
	}
	return ""
}

// localZoneName resolves the IANA name of the system time zone from the
// /etc/localtime symlink (Go's time.Local doesn't expose it).
func localZoneName() string {
	link, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	if i := strings.Index(link, "zoneinfo/"); i >= 0 {
		return link[i+len("zoneinfo/"):]
	}
	return ""
}

// zoneTabCountry scans zone.tab ("FI	+6010+02458	Europe/Helsinki") for the
// country of the given zone.
func zoneTabCountry(r io.Reader, zone string) string {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) >= 3 && fields[2] == zone {
			return strings.ToUpper(fields[0])
		}
	}
	return ""
}
