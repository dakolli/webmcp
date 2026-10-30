package recon

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// naabuPin is v2.6.1, the newest naabu tag whose go directive (1.24.0) is
// compatible with this host's Go 1.26.4. v2.7.0 declares go 1.26.10 and fails to
// install here, so the pin stays one release back rather than forcing a
// toolchain switch. Resolved with `go list -m -json` on 2026-10-30.
const naabuPin = "v2.6.1"

// naabuSpec pins naabu, the port scanner exposed as recon_ports. The private
// GOBIN installs it with CGO disabled, so CONNECT and UDP work and SYN does not.
var naabuSpec = Spec{
	ID:          "recon_ports",
	Module:      "github.com/projectdiscovery/naabu/v2/cmd/naabu",
	Pin:         naabuPin,
	BinName:     "naabu",
	VersionArgs: []string{"-version"},
	JSONArgs:    []string{"-json", "-silent", "-nc"},
	Doc:         naabuDoc,
}

func init() {
	Register(naabuSpec)
}

// naabuDoc is the scanner-specific description shown with recon_ports.
const naabuDoc = "Port-scans hosts with naabu and returns one entry per open port as host, ip and port. " +
	"CONNECT is the default and works on this CGO-disabled build; mode udp also works. " +
	"SYN is unavailable: naabu needs a libpcap-capable build and this binary is installed with CGO_ENABLED=0. " +
	"ports defaults to the top 100; an explicit list such as 80,443,8000-8100 is capped at 200 ports. " +
	"targets accept hosts and CIDRs (500 expanded hosts per call; a CIDR wider than /24 is refused). " +
	"authorized:true is required and is never defaulted. passive sends no packets and uses shodan. " +
	"Example: {\"targets\":[\"scanme.nmap.org\"],\"authorized\":true}."

// PortsScan is one validated naabu port scan. Targets are hosts, CIDRs or ASNs;
// Ports is a comma-separated list of ports and ranges; Mode is connect, udp or
// syn. An empty Ports selects the top 100 and an empty Mode selects connect.
type PortsScan struct {
	Targets      []string
	Ports        string
	Mode         string
	ExcludePorts string
	ExcludeCDN   bool
	Passive      bool
	Rate         int
	Threads      int
}

// Args validates s and returns the naabu argv. Targets go through -host because
// naabu v2.6.1 ignores trailing positional hosts and exits with "no input list
// provided". A SYN request fails because this build has no libpcap; CONNECT and
// UDP always work.
func (s PortsScan) Args() ([]string, error) {
	if len(s.Targets) == 0 {
		return nil, fmt.Errorf("recon_ports: invalid arguments: targets is required — pass at least one host, CIDR, or ASN")
	}
	if err := checkCIDRHosts(s.Targets); err != nil {
		return nil, err
	}
	if err := checkPortList(s.Ports); err != nil {
		return nil, err
	}
	if s.Rate < 0 {
		return nil, fmt.Errorf("recon_ports: invalid arguments: rate must be >= 0")
	}
	if s.Threads < 0 {
		return nil, fmt.Errorf("recon_ports: invalid arguments: threads must be >= 0")
	}
	mode, err := scanMode(s.Mode)
	if err != nil {
		return nil, err
	}

	args := make([]string, 0, 16)
	if mode == "udp" {
		args = append(args, "-u")
	} else {
		args = append(args, "-scan-type", "connect")
	}
	if ports := strings.TrimSpace(s.Ports); ports == "" {
		args = append(args, "-top-ports", "100")
	} else {
		args = append(args, "-p", ports)
	}
	if exclude := strings.TrimSpace(s.ExcludePorts); exclude != "" {
		args = append(args, "-exclude-ports", exclude)
	}
	if s.ExcludeCDN {
		args = append(args, "-exclude-cdn")
	}
	if s.Passive {
		args = append(args, "-passive")
	}
	if s.Rate > 0 {
		args = append(args, "-rate", strconv.Itoa(s.Rate))
	}
	if s.Threads > 0 {
		args = append(args, "-c", strconv.Itoa(s.Threads))
	}
	return append(args, "-host", strings.Join(s.Targets, ",")), nil
}

// PortEntryKey identifies one naabu result by host, ip and port so a port the
// scanner reports twice collapses to one entry while an IPv4 and an IPv6 answer
// for the same port stay distinct. A result lacking all three returns "" to opt
// out of deduplication.
func PortEntryKey(entry map[string]any) string {
	host, _ := entry["host"].(string)
	ip, _ := entry["ip"].(string)
	port := portText(entry["port"])
	if host == "" && ip == "" && port == "" {
		return ""
	}
	return strings.Join([]string{host, ip, port}, "\x00")
}

// portText renders a decoded port field as text, accepting the JSON number and
// string forms naabu emits.
func portText(value any) string {
	switch v := value.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

// scanMode resolves the requested scan type. SYN is refused because the pinned
// naabu is built with CGO disabled and therefore has no libpcap.
func scanMode(raw string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(raw)); mode {
	case "", "connect":
		return "connect", nil
	case "udp":
		return "udp", nil
	case "syn":
		return "", fmt.Errorf(
			"recon_ports: invalid arguments: mode syn needs a libpcap-capable naabu build, but this binary is installed with CGO_ENABLED=0 — use mode connect (default) or mode udp, both of which work without libpcap",
		)
	default:
		return "", fmt.Errorf(
			"recon_ports: invalid arguments: mode %q is not supported — use connect (default) or udp; syn is unavailable on this build",
			raw,
		)
	}
}

// checkCIDRHosts refuses a target whose expanded host count exceeds MaxHosts
// before any packet is sent. A bare host counts as one address.
func checkCIDRHosts(targets []string) error {
	total := 0
	for _, raw := range targets {
		target := strings.TrimSpace(raw)
		if target == "" {
			continue
		}
		count := 1
		if strings.Contains(target, "/") {
			_, network, err := net.ParseCIDR(target)
			if err != nil {
				return fmt.Errorf("recon_ports: invalid arguments: target %q is not a valid CIDR: %w", target, err)
			}
			ones, bits := network.Mask.Size()
			hostBits := bits - ones
			if hostBits >= 31 {
				return fmt.Errorf(
					"recon_ports: target %s expands beyond the %d-host cap — narrow the CIDR to /24 or split the call",
					target,
					MaxHosts,
				)
			}
			count = 1 << hostBits
			if count > MaxHosts {
				return fmt.Errorf(
					"recon_ports: target %s expands to %d hosts, over the %d cap — narrow the CIDR to /24 or split the call",
					target,
					count,
					MaxHosts,
				)
			}
		}
		total += count
	}
	if total > MaxHosts {
		return fmt.Errorf(
			"recon_ports: targets expand to %d hosts, over the %d cap — split the call or pass output_file for a larger report",
			total,
			MaxHosts,
		)
	}
	return nil
}

// checkPortList validates an explicit port list and refuses more than MaxPorts.
// An empty list means the top 100 and needs no cap.
func checkPortList(spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	total := 0
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("recon_ports: invalid arguments: ports %q has an empty entry", spec)
		}
		lo, hi, err := portRange(part)
		if err != nil {
			return err
		}
		total += hi - lo + 1
	}
	if total > MaxPorts {
		return capError(total, "ports", MaxPorts)
	}
	return nil
}

// portRange parses one port or a low-high range and returns its bounds.
func portRange(part string) (int, int, error) {
	if !strings.Contains(part, "-") {
		port, err := parsePort(part)
		if err != nil {
			return 0, 0, err
		}
		return port, port, nil
	}
	bounds := strings.SplitN(part, "-", 2)
	lo, err := parsePort(bounds[0])
	if err != nil {
		return 0, 0, err
	}
	hi, err := parsePort(bounds[1])
	if err != nil {
		return 0, 0, err
	}
	if lo > hi {
		return 0, 0, fmt.Errorf("recon_ports: invalid arguments: port range %q is descending", part)
	}
	return lo, hi, nil
}

// parsePort parses one port number and requires it to be in 1..65535.
func parsePort(text string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("recon_ports: invalid arguments: port %q is not a number in 1..65535", text)
	}
	return port, nil
}
