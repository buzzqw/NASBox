// Package discover finds a NAS on the local network without any setup: it asks
// the network with mDNS (what Synology, QNAP and most NAS advertise as
// "name.local") and, as a safety net, looks for hosts that answer on the SSH
// port in the PC's own subnet. Standard library only.
package discover

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Candidate is a machine that could be the NAS.
type Candidate struct {
	IP     string // dotted IPv4
	Name   string // mDNS or reverse-DNS name, "" when unknown
	Banner string // SSH banner when the SSH port answered
	MDNS   bool   // advertised itself over mDNS
}

// Label is the text shown to the user: "DS420j · 192.168.1.119".
func (c Candidate) Label() string {
	if c.Name != "" {
		return c.Name + " · " + c.IP
	}
	return c.IP
}

// Host is the address to configure: the IP, which survives a missing DNS.
func (c Candidate) Host() string { return c.IP }

// Find runs both discoveries for at most `wait` and returns the merged list,
// machines that advertised themselves first.
func Find(ctx context.Context, wait time.Duration) []Candidate {
	if wait <= 0 {
		wait = 4 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var (
		mu     sync.Mutex
		merged = map[string]*Candidate{}
		group  sync.WaitGroup
	)
	own := ownAddresses()
	add := func(candidate Candidate) {
		if own[candidate.IP] {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		known, ok := merged[candidate.IP]
		if !ok {
			copy := candidate
			merged[candidate.IP] = &copy
			return
		}
		if known.Name == "" {
			known.Name = candidate.Name
		}
		if known.Banner == "" {
			known.Banner = candidate.Banner
		}
		known.MDNS = known.MDNS || candidate.MDNS
	}
	group.Add(2)
	go func() { defer group.Done(); MDNS(ctx, add) }()
	go func() { defer group.Done(); ScanSSH(ctx, add) }()
	group.Wait()
	out := make([]Candidate, 0, len(merged))
	for _, candidate := range merged {
		out = append(out, *candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MDNS != out[j].MDNS {
			return out[i].MDNS
		}
		if (out[i].Banner != "") != (out[j].Banner != "") {
			return out[i].Banner != ""
		}
		return out[i].IP < out[j].IP
	})
	return out
}

// ---------------------------------------------------------------------------
// SSH scan of the local /24
// ---------------------------------------------------------------------------

// localSubnets returns the private IPv4 networks of the up interfaces, each
// clipped to at most a /24 (254 hosts): a scan must stay quick and polite.
func localSubnets() []*net.IPNet {
	var out []*net.IPNet
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			network, ok := address.(*net.IPNet)
			if !ok {
				continue
			}
			ip := network.IP.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			mask := network.Mask
			if ones, _ := mask.Size(); ones < 24 {
				mask = net.CIDRMask(24, 32)
			}
			out = append(out, &net.IPNet{IP: ip.Mask(mask), Mask: mask})
		}
	}
	return out
}

// ownAddresses are this PC's own IPv4 addresses: never offered as a NAS.
func ownAddresses() map[string]bool {
	own := map[string]bool{}
	addresses, _ := net.InterfaceAddrs()
	for _, address := range addresses {
		if network, ok := address.(*net.IPNet); ok {
			own[network.IP.String()] = true
		}
	}
	return own
}

// hostsOf lists the usable addresses of a /24-or-smaller network.
func hostsOf(network *net.IPNet) []net.IP {
	var hosts []net.IP
	start := binary.BigEndian.Uint32(network.IP.To4())
	ones, bits := network.Mask.Size()
	size := uint32(1) << uint(bits-ones)
	for offset := uint32(1); offset+1 < size; offset++ {
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, start+offset)
		hosts = append(hosts, ip)
	}
	return hosts
}

// ScanSSH reports every host of the local subnet whose port 22 answers with an
// SSH banner.
func ScanSSH(ctx context.Context, found func(Candidate)) {
	own := ownAddresses()
	var group sync.WaitGroup
	slots := make(chan struct{}, 64)
	for _, network := range localSubnets() {
		for _, ip := range hostsOf(network) {
			if own[ip.String()] {
				continue
			}
			select {
			case <-ctx.Done():
				group.Wait()
				return
			case slots <- struct{}{}:
			}
			group.Add(1)
			go func(ip net.IP) {
				defer group.Done()
				defer func() { <-slots }()
				dialer := net.Dialer{Timeout: 600 * time.Millisecond}
				conn, err := dialer.DialContext(ctx, "tcp4", net.JoinHostPort(ip.String(), "22"))
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
				banner, _ := bufio.NewReader(conn).ReadString('\n')
				banner = strings.TrimSpace(banner)
				if !strings.HasPrefix(banner, "SSH-") {
					return
				}
				name := ""
				lookup, cancelLookup := context.WithTimeout(ctx, 700*time.Millisecond)
				if names, err := net.DefaultResolver.LookupAddr(lookup, ip.String()); err == nil && len(names) > 0 {
					name = strings.TrimSuffix(names[0], ".")
				}
				cancelLookup()
				found(Candidate{IP: ip.String(), Name: name, Banner: banner})
			}(ip)
		}
	}
	group.Wait()
}

// ---------------------------------------------------------------------------
// mDNS (RFC 6762), just enough to learn "name.local -> IPv4"
// ---------------------------------------------------------------------------

var mdnsGroup = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// services asked for: the ones NAS advertise.
var mdnsServices = []string{
	"_smb._tcp.local.", "_ssh._tcp.local.", "_sftp-ssh._tcp.local.",
	"_afpovertcp._tcp.local.", "_device-info._tcp.local.", "_http._tcp.local.",
}

// buildQuery encodes one question per service (PTR, class IN with the
// unicast-response bit set so replies come straight back to this socket).
func buildQuery(names []string) []byte {
	packet := make([]byte, 12)
	binary.BigEndian.PutUint16(packet[4:], uint16(len(names)))
	for _, name := range names {
		for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
			packet = append(packet, byte(len(label)))
			packet = append(packet, label...)
		}
		packet = append(packet, 0)
		packet = binary.BigEndian.AppendUint16(packet, 12)     // PTR
		packet = binary.BigEndian.AppendUint16(packet, 0x8001) // IN + unicast response
	}
	return packet
}

// readName decodes a possibly compressed DNS name starting at offset and
// returns it with the offset after the name.
func readName(packet []byte, offset int) (string, int, error) {
	var labels []string
	next, jumped := -1, false
	for hops := 0; hops < 32; hops++ {
		if offset >= len(packet) {
			return "", 0, errors.New("dns: name past end")
		}
		length := int(packet[offset])
		switch {
		case length == 0:
			if !jumped {
				next = offset + 1
			}
			return strings.Join(labels, "."), next, nil
		case length&0xC0 == 0xC0:
			if offset+1 >= len(packet) {
				return "", 0, errors.New("dns: bad pointer")
			}
			pointer := (length&0x3F)<<8 | int(packet[offset+1])
			if !jumped {
				next = offset + 2
			}
			jumped = true
			offset = pointer
		default:
			if offset+1+length > len(packet) {
				return "", 0, errors.New("dns: label past end")
			}
			labels = append(labels, string(packet[offset+1:offset+1+length]))
			offset += 1 + length
		}
	}
	return "", 0, errors.New("dns: too many hops")
}

// parseAddresses extracts name -> IPv4 from the A records of a response.
func parseAddresses(packet []byte) (map[string]string, error) {
	if len(packet) < 12 {
		return nil, errors.New("dns: short packet")
	}
	questions := int(binary.BigEndian.Uint16(packet[4:]))
	records := int(binary.BigEndian.Uint16(packet[6:])) +
		int(binary.BigEndian.Uint16(packet[8:])) + int(binary.BigEndian.Uint16(packet[10:]))
	offset := 12
	for index := 0; index < questions; index++ {
		_, next, err := readName(packet, offset)
		if err != nil {
			return nil, err
		}
		offset = next + 4
	}
	out := map[string]string{}
	for index := 0; index < records; index++ {
		name, next, err := readName(packet, offset)
		if err != nil || next+10 > len(packet) {
			return out, err
		}
		recordType := binary.BigEndian.Uint16(packet[next:])
		length := int(binary.BigEndian.Uint16(packet[next+8:]))
		data := next + 10
		if data+length > len(packet) {
			return out, errors.New("dns: record past end")
		}
		if recordType == 1 && length == 4 { // A
			out[strings.ToLower(name)] = net.IP(packet[data : data+4]).String()
		}
		offset = data + length
	}
	return out, nil
}

// MDNS asks the local network for NAS-like services and reports each machine
// that answers with an IPv4 address.
func MDNS(ctx context.Context, found func(Candidate)) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return
	}
	defer conn.Close()
	query := buildQuery(mdnsServices)
	send := func() { _, _ = conn.WriteToUDP(query, mdnsGroup) }
	send()
	resend := time.NewTicker(1200 * time.Millisecond)
	defer resend.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				return
			case <-resend.C:
				send()
			}
		}
	}()
	buffer := make([]byte, 9000)
	for {
		size, from, err := conn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		addresses, _ := parseAddresses(buffer[:size])
		for name, ip := range addresses {
			label := strings.TrimSuffix(name, ".local")
			if ip == "" || strings.HasPrefix(ip, "169.254.") {
				continue
			}
			_ = from
			found(Candidate{IP: ip, Name: label, MDNS: true})
		}
	}
}

// Describe is a one-line summary used in logs and tests.
func Describe(candidates []Candidate) string {
	parts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		parts = append(parts, candidate.Label())
	}
	return fmt.Sprintf("%d: %s", len(candidates), strings.Join(parts, ", "))
}
