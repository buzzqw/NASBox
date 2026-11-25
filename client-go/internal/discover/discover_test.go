package discover

import (
	"encoding/binary"
	"net"
	"testing"
)

// response builds a tiny mDNS answer: DS420j.local A 192.168.1.119.
func response() []byte {
	packet := make([]byte, 12)
	binary.BigEndian.PutUint16(packet[6:], 1) // one answer
	for _, label := range []string{"DS420j", "local"} {
		packet = append(packet, byte(len(label)))
		packet = append(packet, label...)
	}
	packet = append(packet, 0)
	packet = binary.BigEndian.AppendUint16(packet, 1) // A
	packet = binary.BigEndian.AppendUint16(packet, 1) // IN
	packet = binary.BigEndian.AppendUint32(packet, 120)
	packet = binary.BigEndian.AppendUint16(packet, 4)
	return append(packet, 192, 168, 1, 119)
}

func TestParseAddressesReadsARecords(t *testing.T) {
	got, err := parseAddresses(response())
	if err != nil || got["ds420j.local"] != "192.168.1.119" {
		t.Fatalf("addresses: %v %v", got, err)
	}
}

func TestReadNameFollowsCompressionPointers(t *testing.T) {
	packet := make([]byte, 12)
	packet = append(packet, 3, 'n', 'a', 's', 5, 'l', 'o', 'c', 'a', 'l', 0) // name at 12
	packet = append(packet, 0xC0, 12)                                        // pointer to it
	name, next, err := readName(packet, 12+11)
	if err != nil || name != "nas.local" || next != len(packet) {
		t.Fatalf("name %q next %d err %v", name, next, err)
	}
}

func TestParseAddressesRejectsGarbage(t *testing.T) {
	for _, packet := range [][]byte{nil, {1, 2, 3}, append(make([]byte, 12), 0xC0, 0xFF)} {
		if _, err := parseAddresses(packet); err == nil && len(packet) < 12 {
			t.Fatalf("garbage accepted: %v", packet)
		}
	}
}

func TestBuildQueryAsksTheRequestedServices(t *testing.T) {
	query := buildQuery([]string{"_smb._tcp.local."})
	if binary.BigEndian.Uint16(query[4:]) != 1 || query[12] != 4 || string(query[13:17]) != "_smb" {
		t.Fatalf("query: %v", query)
	}
}

func TestHostsOfSlash24(t *testing.T) {
	_, network, _ := net.ParseCIDR("192.168.1.0/24")
	hosts := hostsOf(network)
	if len(hosts) != 254 || hosts[0].String() != "192.168.1.1" || hosts[253].String() != "192.168.1.254" {
		t.Fatalf("hosts: %d %v..%v", len(hosts), hosts[0], hosts[len(hosts)-1])
	}
}
