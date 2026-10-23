package template

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

const onPremHAProxyConfigTemplatePath = "/etc/kubernetes/static-pod-resources/haproxy/haproxy.cfg.tmpl"

// clientGoTLSHandshakeTimeout is the TLS handshake timeout of client-go's
// default transport (net/http.DefaultTransport). A client connection that
// haproxy cannot hand over to a reachable master within it fails with
// "net/http: TLS handshake timeout".
const clientGoTLSHandshakeTimeout = 10 * time.Second

// initialSYNRetransmissionTimeout is the initial TCP SYN retransmission
// timeout on Linux (RFC 6298). A connect timeout at or below it gives up on a
// reachable master as soon as a single SYN is lost.
const initialSYNRetransmissionTimeout = time.Second

// TestOnPremAPIHAProxyRedispatchesUnreachableMasters verifies that the API VIP
// load balancer moves a connection it cannot establish to one master over to
// another master before client-go gives up on the TLS handshake, even when the
// second master it tries is unreachable as well (OCPBUGS-72554).
func TestOnPremAPIHAProxyRedispatchesUnreachableMasters(t *testing.T) {
	for _, platform := range []string{"baremetal", "vsphere", "openstack", "nutanix"} {
		t.Run(platform, func(t *testing.T) {
			masters := parseHAProxyBackend(t, renderOnPremHAProxyConfigTemplate(t, platform), "masters")

			if masters.redispatchInterval != 1 {
				t.Errorf("backend masters must redispatch on every connection retry (option redispatch 1), got redispatch interval %d (0 means disabled)", masters.redispatchInterval)
			}
			if masters.retries < 2 {
				t.Errorf("backend masters needs at least 2 connection retries to get past two unreachable masters, got %d", masters.retries)
			}
			if masters.connectTimeout <= initialSYNRetransmissionTimeout {
				t.Errorf("backend masters timeout connect %s must allow one SYN retransmission (> %s)", masters.connectTimeout, initialSYNRetransmissionTimeout)
			}
			if 2*masters.connectTimeout >= clientGoTLSHandshakeTimeout {
				t.Errorf("two connection attempts to unreachable masters (2 x timeout connect %s) must end before client-go's TLS handshake timeout (%s)", masters.connectTimeout, clientGoTLSHandshakeTimeout)
			}
		})
	}
}

func renderOnPremHAProxyConfigTemplate(t *testing.T, platform string) string {
	t.Helper()

	controllerConfig, err := controllerConfigFromFile(configs[platform])
	if err != nil {
		t.Fatalf("failed to read controller config for %s: %v", platform, err)
	}
	cfgs, err := generateTemplateMachineConfigs(&RenderConfig{
		ControllerConfigSpec: &controllerConfig.Spec,
		PullSecret:           `{"dummy":"dummy"}`,
	}, templateDir)
	if err != nil {
		t.Fatalf("failed to generate machine configs for %s: %v", platform, err)
	}

	for _, cfg := range cfgs {
		ign, err := ctrlcommon.ParseAndConvertConfig(cfg.Spec.Config.Raw)
		if err != nil {
			t.Fatalf("failed to parse ignition config of %s: %v", cfg.Name, err)
		}
		for _, file := range ign.Storage.Files {
			if file.Path != onPremHAProxyConfigTemplatePath {
				continue
			}
			contents, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
			if err != nil {
				t.Fatalf("failed to decode %s from %s: %v", file.Path, cfg.Name, err)
			}
			return string(contents)
		}
	}
	t.Fatalf("no machine config rendered %s for %s", onPremHAProxyConfigTemplatePath, platform)
	return ""
}

// haproxyBackend holds the effective connection retry settings of a backend,
// after applying what it inherits from the defaults section.
type haproxyBackend struct {
	retries int
	// connectTimeout is zero when no connect timeout applies (haproxy then
	// waits forever).
	connectTimeout time.Duration
	// redispatchInterval follows "option redispatch [<interval>]": 0 when
	// redispatch is disabled, -1 (the default interval) for the last retry only,
	// and N > 0 for every Nth retry.
	redispatchInterval int
}

var haproxySectionKeywords = map[string]bool{
	"global": true, "defaults": true, "frontend": true, "backend": true, "listen": true,
	"peers": true, "resolvers": true, "userlist": true, "mailers": true, "program": true,
	"http-errors": true, "ring": true, "cache": true,
}

func parseHAProxyBackend(t *testing.T, cfg, name string) haproxyBackend {
	t.Helper()

	// haproxy defaults to 3 retries and no redispatch.
	defaults := haproxyBackend{retries: 3}
	var backend haproxyBackend
	section, found := "", false

	scanner := bufio.NewScanner(strings.NewReader(cfg))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") || strings.HasPrefix(fields[0], "{{") {
			continue
		}
		if haproxySectionKeywords[fields[0]] {
			section = strings.Join(fields, " ")
			if section == "backend "+name {
				backend, found = defaults, true
			}
			continue
		}
		var target *haproxyBackend
		switch {
		case section == "defaults":
			target = &defaults
		case section == "backend "+name:
			target = &backend
		default:
			continue
		}
		if err := applyHAProxyDirective(target, fields); err != nil {
			t.Fatalf("section %q: %v", section, err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("failed to read haproxy config: %v", err)
	}
	if !found {
		t.Fatalf("haproxy config has no backend %q:\n%s", name, cfg)
	}
	return backend
}

func applyHAProxyDirective(b *haproxyBackend, fields []string) error {
	switch {
	case fields[0] == "retries" && len(fields) == 2:
		retries, err := strconv.Atoi(fields[1])
		if err != nil {
			return fmt.Errorf("invalid retries %q: %w", fields[1], err)
		}
		b.retries = retries
	case fields[0] == "timeout" && len(fields) == 3 && fields[1] == "connect":
		timeout, err := parseHAProxyTime(fields[2])
		if err != nil {
			return err
		}
		b.connectTimeout = timeout
	case fields[0] == "option" && len(fields) >= 2 && fields[1] == "redispatch":
		b.redispatchInterval = -1
		if len(fields) == 3 {
			interval, err := strconv.Atoi(fields[2])
			if err != nil {
				return fmt.Errorf("invalid redispatch interval %q: %w", fields[2], err)
			}
			b.redispatchInterval = interval
		}
	case fields[0] == "no" && len(fields) >= 3 && fields[1] == "option" && fields[2] == "redispatch":
		b.redispatchInterval = 0
	}
	return nil
}

// parseHAProxyTime parses an haproxy time value, which is in milliseconds
// unless a unit is given.
func parseHAProxyTime(value string) (time.Duration, error) {
	units := []struct {
		suffix string
		unit   time.Duration
	}{
		{"us", time.Microsecond}, {"ms", time.Millisecond}, {"s", time.Second},
		{"m", time.Minute}, {"h", time.Hour}, {"d", 24 * time.Hour},
	}
	number, unit := value, time.Millisecond
	for _, u := range units {
		if strings.HasSuffix(value, u.suffix) {
			number, unit = strings.TrimSuffix(value, u.suffix), u.unit
			break
		}
	}
	n, err := strconv.Atoi(number)
	if err != nil {
		return 0, fmt.Errorf("invalid haproxy time %q: %w", value, err)
	}
	return time.Duration(n) * unit, nil
}
