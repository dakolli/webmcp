package recon_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/recon"
	recontools "github.com/dakolli/webmcp/pkg/recon/tools"
)

func TestPortsScanArgsCIDRCap(t *testing.T) {
	t.Parallel()

	_, err := (recon.PortsScan{Targets: []string{"10.0.0.0/16"}}).Args()
	if err == nil {
		t.Fatal("a /16 passed the 500-host cap")
	}
	for _, want := range []string{"65536", "500"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}

	if _, err := (recon.PortsScan{Targets: []string{"10.0.0.0/24"}}).Args(); err != nil {
		t.Fatalf("a /24 was refused: %v", err)
	}
}

func TestPortsScanArgsRejectsSYN(t *testing.T) {
	t.Parallel()

	_, err := (recon.PortsScan{Targets: []string{"example.com"}, Mode: "syn"}).Args()
	if err == nil {
		t.Fatal("mode syn was accepted on a CGO-disabled build")
	}
	for _, want := range []string{"libpcap", "connect", "udp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestPortsScanArgsPortCap(t *testing.T) {
	t.Parallel()

	ports := strings.TrimSuffix(strings.Repeat("1,", 201), ",")
	_, err := (recon.PortsScan{Targets: []string{"example.com"}, Ports: ports}).Args()
	if err == nil {
		t.Fatal("201 ports passed the 200-port cap")
	}
	for _, want := range []string{"201", "output_file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestPortsScanArgsUsesHostFlag(t *testing.T) {
	t.Parallel()

	spec, ok := recon.Get("recon_ports")
	if !ok {
		t.Fatal("recon_ports is not registered")
	}
	if spec.Pin != "v2.6.1" {
		t.Errorf("naabu pin = %q, want v2.6.1 (v2.7.0 needs Go 1.26.10)", spec.Pin)
	}

	args, err := (recon.PortsScan{
		Targets: []string{"example.com", "example.org"},
		Ports:   "80,443",
	}).Args()
	if err != nil {
		t.Fatalf("Args: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-host example.com,example.org") {
		t.Fatalf("args %q do not pass targets through -host", joined)
	}
	for _, arg := range args {
		if arg == "example.com" || arg == "example.org" {
			t.Fatalf("args %q still carry a bare positional target", joined)
		}
	}
}

func TestPortsExecuteCompactsStubOutput(t *testing.T) {
	out := executePortsStub(t, `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "naabu version 2.7.0"
  exit 0
fi
printf '%s\n' '{"host":"scanme.nmap.org","ip":"45.33.32.156","port":22,"tls":false}' '{"host":"scanme.nmap.org","ip":"45.33.32.156","port":80}'
`)
	env := decodePortsEnvelope(t, out)
	if env.Data.Count != 2 || len(env.Data.Entries) != 2 {
		t.Fatalf("count=%d entries=%d, want 2/2 (%s)", env.Data.Count, len(env.Data.Entries), out)
	}
	first := env.Data.Entries[0]
	if first["host"] != "scanme.nmap.org" || first["ip"] != "45.33.32.156" || first["port"] != float64(22) {
		t.Errorf("first entry = %v", first)
	}
	if _, ok := first["tls"]; ok {
		t.Errorf("entry kept the non-contract field tls: %v", first)
	}
}

func TestPortsExecuteDeduplicatesOpenPorts(t *testing.T) {
	out := executePortsStub(t, `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "naabu version 2.6.1"
  exit 0
fi
printf '%s\n' '{"host":"scanme.nmap.org","ip":"45.33.32.156","port":22}' '{"host":"scanme.nmap.org","ip":"45.33.32.156","port":22}' '{"host":"scanme.nmap.org","ip":"45.33.32.156","port":80}' '{"host":"scanme.nmap.org","ip":"45.33.32.156","port":80}' '{"host":"scanme.nmap.org","ip":"2600:3c01::f03c:91ff:fe18:bb2f","port":80}'
`)
	env := decodePortsEnvelope(t, out)
	if env.Data.Count != 3 || len(env.Data.Entries) != 3 {
		t.Fatalf("count=%d entries=%d, want 3 unique ports (%s)", env.Data.Count, len(env.Data.Entries), out)
	}
	seen := make(map[string]struct{}, len(env.Data.Entries))
	for _, entry := range env.Data.Entries {
		host, _ := entry["host"].(string)
		ip, _ := entry["ip"].(string)
		port, _ := entry["port"].(float64)
		seen[strings.Join([]string{host, ip, strconv.FormatFloat(port, 'f', -1, 64)}, "|")] = struct{}{}
	}
	if len(seen) != 3 {
		t.Errorf("decoded entries are not unique: %v", env.Data.Entries)
	}
	if _, ok := seen["scanme.nmap.org|2600:3c01::f03c:91ff:fe18:bb2f|80"]; !ok {
		t.Errorf("the IPv6 answer for port 80 was dropped: %v", env.Data.Entries)
	}
}

// portsEnvelope is the decoded recon_ports success envelope.
type portsEnvelope struct {
	OK   bool `json:"ok"`
	Data struct {
		Entries []map[string]any `json:"entries"`
		Count   int              `json:"count"`
	} `json:"data"`
}

// executePortsStub installs a fake naabu whose run emits script's printf output
// and returns the recon_ports envelope.
func executePortsStub(t *testing.T, script string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	binDir := filepath.Join(home, "browser-mcp", "recon", "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "naabu"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	out, err := recontools.NewPorts().Execute(
		context.Background(),
		json.RawMessage(`{"targets":["scanme.nmap.org"],"authorized":true}`),
	)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return out
}

// decodePortsEnvelope decodes a recon_ports success envelope or fails the test.
func decodePortsEnvelope(t *testing.T, out string) portsEnvelope {
	t.Helper()
	var env portsEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", out, err)
	}
	if !env.OK {
		t.Fatalf("envelope is not ok: %s", out)
	}
	return env
}
