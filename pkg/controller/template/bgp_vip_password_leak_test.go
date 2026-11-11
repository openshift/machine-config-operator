package template

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
)

// captureKlog redirects klog to a buffer at verbosity 4 for the duration of
// the test, so the controller's error logging can be inspected.
func captureKlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	var verbosity klog.Level
	require.NoError(t, verbosity.Set("4"))
	klog.LogToStderr(false)
	klog.SetOutput(&buf)
	t.Cleanup(func() {
		klog.Flush()
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
		require.NoError(t, verbosity.Set("0"))
	})
	return &buf
}

// TestBGPVIPPasswordNeverSurfaces drives a BGP VIP password that cannot be
// rendered safely through the real template controller sync: a password with
// a YAML line break (U+0085) used to break the rendered peers file and the
// resulting unmarshal error echoed the whole file, password included, into
// the sync error, the ControllerConfig status and the logs; a password with
// invalid UTF-8 used to be replaced silently by json.Marshal. Both must be
// rejected, and the password must appear in no returned error, status write
// (whole object), event or log line; no MachineConfig is written.
func TestBGPVIPPasswordNeverSurfaces(t *testing.T) {
	const marker = "SYNTHETIC LEAK"
	lastGood := renderMCs(t, bgpControllerConfig(t, fullPeersTransport, true), legacyFullPeersFile)
	for name, password := range map[string][]byte{
		"NEL (U+0085) breaks the YAML block scalar": []byte(marker + "-\u0085-MARKER"),
		"DEL (U+007F)":        []byte(marker + "-\u007f-MARKER"),
		"C1 control (U+0080)": []byte(marker + "-\u0080-MARKER"),
		"noncharacter U+FFFF": []byte(marker + "-\uffff-MARKER"),
		"invalid UTF-8":       []byte(marker + "-\xff-MARKER"),
	} {
		t.Run(name, func(t *testing.T) {
			cc := bgpControllerConfig(t, fullPeersTransport, true)
			f := bgpFixture(t, cc, []*corev1.Secret{bgpPasswordSecret("openshift-config", "bgp-peer-pw", password)}, lastGood)
			c := f.newController()
			recorder := record.NewFakeRecorder(100)
			c.eventRecorder = recorder
			logs := captureKlog(t)

			key := getKey(cc, t)
			err := c.syncHandler(key)
			// The worker's error handling logs the sync error.
			c.handleErr(err, key)
			klog.Flush()

			require.Error(t, err, "an unsafe password must fail the sync")
			assert.NotContains(t, err.Error(), marker)

			created, updated := writtenMachineConfigs(f.client.Actions())
			assert.Empty(t, created, "no MachineConfig may be written")
			assert.Empty(t, updated, "no MachineConfig may be written")

			statuses := controllerConfigStatusWrites(f.client.Actions())
			require.NotEmpty(t, statuses)
			assert.NotEmpty(t, lastConditionMessage(statuses, mcfgv1.TemplateControllerFailing))
			for _, status := range statuses {
				data, jsonErr := json.Marshal(status)
				require.NoError(t, jsonErr)
				assert.NotContains(t, string(data), marker)
			}
			close(recorder.Events)
			for event := range recorder.Events {
				assert.NotContains(t, event, marker)
			}
			assert.NotContains(t, logs.String(), marker)
		})
	}
}
