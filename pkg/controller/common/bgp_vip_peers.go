package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"unicode"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// bgpVIPPasswordMaxBytes is the kernel's TCP MD5 password limit (RFC 2385,
// TCP_MD5SIG_MAXKEYLEN).
const bgpVIPPasswordMaxBytes = 80

// BGPVIPPasswordSecretRef locates the kubernetes.io/basic-auth Secret holding
// a peer's TCP MD5 password. It is what the operator writes into the
// ControllerConfig transport instead of the password itself; the namespace is
// always openshift-config.
type BGPVIPPasswordSecretRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// BGPVIPPeer is one peer of the BGP VIP peers payload. The same type serves
// two wire forms:
//   - the transport carried by ControllerConfigSpec.BGPVIPPeersJSON, where a
//     password-protected peer has PasswordSecret set and Password empty;
//   - the node file /etc/kubernetes/static-pod-resources/frr-k8s/frr-peers.json
//     consumed by `runtimecfg render --peer-file`, where Password holds the
//     resolved plaintext and PasswordSecret is nil.
//
// Field order, spellings and value encodings ("true" for enabled knobs, bare
// decimal seconds for timers) must not change: with PasswordSecret unset the
// node file is byte-identical to the Dev Preview payload runtimecfg consumes.
type BGPVIPPeer struct {
	PeerAddress    string                   `json:"peerAddress"`
	PeerASN        int64                    `json:"peerASN"`
	Password       string                   `json:"password,omitempty"`
	PasswordSecret *BGPVIPPasswordSecretRef `json:"passwordSecret,omitempty"`
	Port           int32                    `json:"port,omitempty"`
	BFDEnabled     string                   `json:"bfdEnabled,omitempty"`
	EBGPMultiHop   string                   `json:"ebgpMultiHop,omitempty"`
	HoldTime       string                   `json:"holdTime,omitempty"`
	KeepaliveTime  string                   `json:"keepaliveTime,omitempty"`
}

// BGPVIPPeersConfig is the top level of the BGP VIP peers payload.
type BGPVIPPeersConfig struct {
	LocalASN      int64                   `json:"localASN"`
	DefaultPeers  []BGPVIPPeer            `json:"defaultPeers"`
	Communities   []string                `json:"communities,omitempty"`
	APIVIPs       []string                `json:"apiVIPs,omitempty"`
	IngressVIPs   []string                `json:"ingressVIPs,omitempty"`
	HostOverrides map[string][]BGPVIPPeer `json:"hostOverrides,omitempty"`
}

// BGPVIPSecretGetter returns the Secret namespace/name. The returned Secret may
// be shared (for example an informer cache object): callers must not modify it.
type BGPVIPSecretGetter func(namespace, name string) (*corev1.Secret, error)

// BGPVIPPasswordFromSecret validates a password Secret and extracts the
// plaintext: type kubernetes.io/basic-auth, non-empty "password" key, at most
// 80 bytes, valid UTF-8 made of printable characters only (unicode.IsPrint:
// no control characters such as U+007F or U+0080..U+009F, no line or
// paragraph separators, no noncharacters such as U+FFFE/U+FFFF). The password
// is embedded in a YAML template and a JSON document on its way to the node,
// so anything else could break the rendered file or be silently replaced.
// Errors name the Secret but never carry its contents.
func BGPVIPPasswordFromSecret(secret *corev1.Secret) (string, error) {
	if secret.Type != corev1.SecretTypeBasicAuth {
		return "", fmt.Errorf("secret %q must be of type %q, got %q", secret.Name, corev1.SecretTypeBasicAuth, secret.Type)
	}
	password := secret.Data[corev1.BasicAuthPasswordKey]
	if len(password) == 0 {
		return "", fmt.Errorf("secret %q must carry a non-empty %q key", secret.Name, corev1.BasicAuthPasswordKey)
	}
	if len(password) > bgpVIPPasswordMaxBytes {
		return "", fmt.Errorf("secret %q key %q exceeds the %d-byte TCP MD5 limit", secret.Name, corev1.BasicAuthPasswordKey, bgpVIPPasswordMaxBytes)
	}
	if !utf8.Valid(password) {
		return "", fmt.Errorf("secret %q key %q must be valid UTF-8", secret.Name, corev1.BasicAuthPasswordKey)
	}
	for _, r := range string(password) {
		if !unicode.IsPrint(r) {
			return "", fmt.Errorf("secret %q key %q must contain only printable characters", secret.Name, corev1.BasicAuthPasswordKey)
		}
	}
	return string(password), nil
}

// SecretWithStringDataMerged returns a deep copy of a Secret read from a
// manifest file with its stringData folded into data. The merge is API-server
// behavior; manifests read straight from disk need it done by hand.
func SecretWithStringDataMerged(secret *corev1.Secret) *corev1.Secret {
	out := secret.DeepCopy()
	for key, value := range out.StringData {
		if out.Data == nil {
			out.Data = map[string][]byte{}
		}
		out.Data[key] = []byte(value)
	}
	out.StringData = nil
	return out
}

// bgpVIPPeerEntry is one peer of a payload together with its locator.
type bgpVIPPeerEntry struct {
	// locator names the peer by position only: "defaultPeers[i]" or
	// "hostOverrides[i].peers[j]", with i counting host overrides in
	// hostname order (the order of the JSON object). It never contains the
	// peer address or the override's hostname.
	locator string
	peer    *BGPVIPPeer
}

// peerEntries lists every peer of the payload, default peers first and then
// host overrides in hostname order, so iteration and errors are
// deterministic. The entries point into c.
func (c *BGPVIPPeersConfig) peerEntries() []bgpVIPPeerEntry {
	entries := make([]bgpVIPPeerEntry, 0, len(c.DefaultPeers))
	for i := range c.DefaultPeers {
		entries = append(entries, bgpVIPPeerEntry{locator: fmt.Sprintf("defaultPeers[%d]", i), peer: &c.DefaultPeers[i]})
	}
	hostnames := make([]string, 0, len(c.HostOverrides))
	for hostname := range c.HostOverrides {
		hostnames = append(hostnames, hostname)
	}
	sort.Strings(hostnames)
	for i, hostname := range hostnames {
		peers := c.HostOverrides[hostname]
		for j := range peers {
			entries = append(entries, bgpVIPPeerEntry{locator: fmt.Sprintf("hostOverrides[%d].peers[%d]", i, j), peer: &peers[j]})
		}
	}
	return entries
}

// parseBGPVIPPeersJSON decodes a peers payload. The decode error is reduced to
// its position or field so that a malformed payload is never echoed into logs
// or status conditions.
func parseBGPVIPPeersJSON(payload string) (*BGPVIPPeersConfig, error) {
	cfg := &BGPVIPPeersConfig{}
	if err := json.Unmarshal([]byte(payload), cfg); err != nil {
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syntaxErr):
			return nil, fmt.Errorf("the BGP VIP peers payload is not valid JSON (offset %d)", syntaxErr.Offset)
		case errors.As(err, &typeErr):
			return nil, fmt.Errorf("the BGP VIP peers payload has an unexpected type for field %q", typeErr.Field)
		default:
			return nil, errors.New("the BGP VIP peers payload could not be decoded")
		}
	}
	return cfg, nil
}

// ResolveBGPVIPPeersJSON turns the ControllerConfig transport into the node
// file content for frr-peers.json: every passwordSecret reference is replaced
// by the password from the referenced openshift-config basic-auth Secret,
// validated by BGPVIPPasswordFromSecret. It is the single validation path for
// the operator (which discards the result), operator bootstrap, the template
// controller and the bootstrap controller.
//
// A transport without references is returned verbatim, so the node file
// stays byte-identical. Otherwise the payload is re-serialized with the same
// field order as the legacy producer.
//
// The function fails closed: a reference outside openshift-config, a
// reference without a name, a peer carrying both an inline password and a
// reference, or an unresolvable or invalid Secret are errors. Errors locate
// the peer by position and name the Secret; they never contain a password,
// a peer address or a hostname.
func ResolveBGPVIPPeersJSON(transport string, getSecret BGPVIPSecretGetter) (string, error) {
	if transport == "" {
		return "", nil
	}
	cfg, err := parseBGPVIPPeersJSON(transport)
	if err != nil {
		return "", err
	}
	entries := cfg.peerEntries()

	hasReferences := false
	for _, e := range entries {
		if e.peer.PasswordSecret != nil {
			hasReferences = true
			break
		}
	}
	if !hasReferences {
		return transport, nil
	}

	resolved := map[types.NamespacedName]string{}
	for _, e := range entries {
		ref := e.peer.PasswordSecret
		if ref == nil {
			continue
		}
		if e.peer.Password != "" {
			return "", fmt.Errorf("the BGP VIP peer %s carries both an inline password and a passwordSecret reference", e.locator)
		}
		if ref.Name == "" {
			return "", fmt.Errorf("the BGP VIP peer %s has a passwordSecret reference without a name", e.locator)
		}
		if ref.Namespace != OpenshiftConfigNamespace {
			return "", fmt.Errorf("the BGP VIP peer %s references a password Secret in namespace %q; only %q is allowed", e.locator, ref.Namespace, OpenshiftConfigNamespace)
		}
		key := types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}
		password, ok := resolved[key]
		if !ok {
			if getSecret == nil {
				return "", fmt.Errorf("failed to resolve the BGP VIP peer password for %s from Secret %s: no Secret source is available", e.locator, key)
			}
			secret, err := getSecret(ref.Namespace, ref.Name)
			if err != nil {
				return "", fmt.Errorf("failed to resolve the BGP VIP peer password for %s from Secret %s: %w", e.locator, key, err)
			}
			if password, err = BGPVIPPasswordFromSecret(secret); err != nil {
				return "", fmt.Errorf("failed to resolve the BGP VIP peer password for %s from Secret %s: %w", e.locator, key, err)
			}
			resolved[key] = password
		}
		e.peer.Password = password
		e.peer.PasswordSecret = nil
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to serialize the BGP VIP peers file: %w", err)
	}
	return string(out), nil
}

// BGPVIPPasswordSecretRefs returns the distinct Secrets referenced by a
// transport, sorted by namespace/name.
func BGPVIPPasswordSecretRefs(transport string) ([]types.NamespacedName, error) {
	if transport == "" {
		return nil, nil
	}
	cfg, err := parseBGPVIPPeersJSON(transport)
	if err != nil {
		return nil, err
	}
	seen := map[types.NamespacedName]struct{}{}
	for _, e := range cfg.peerEntries() {
		if ref := e.peer.PasswordSecret; ref != nil {
			seen[types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}] = struct{}{}
		}
	}
	refs := make([]types.NamespacedName, 0, len(seen))
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
	return refs, nil
}
