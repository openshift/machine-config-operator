package globalpullsecret

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
)

// dockerConfigJSON mirrors the ~/.docker/config.json schema. Extra preserves
// all top-level fields other than auths.
type dockerConfigJSON struct {
	Auths map[string]json.RawMessage `json:"auths,omitempty"`
	Extra map[string]json.RawMessage `json:"-"`
}

func (d dockerConfigJSON) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(d.Extra)+1)
	for k, v := range d.Extra {
		out[k] = v
	}
	out["auths"] = marshalSorted(d.Auths)
	return marshalSorted(out), nil
}

func (d *dockerConfigJSON) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &d.Extra); err != nil {
		return err
	}
	d.Auths = map[string]json.RawMessage{}
	if authsRaw, ok := d.Extra["auths"]; ok {
		if err := json.Unmarshal(authsRaw, &d.Auths); err != nil {
			return fmt.Errorf("parsing auths: %w", err)
		}
		delete(d.Extra, "auths")
	}
	return nil
}

// marshalSorted renders a map of raw JSON values as an object with sorted
// keys, so equivalent inputs always produce byte-identical output.
func marshalSorted(m map[string]json.RawMessage) json.RawMessage {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	buf := []byte("{")
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		key, _ := json.Marshal(k)
		buf = append(buf, key...)
		buf = append(buf, ':')
		buf = append(buf, m[k]...)
	}
	return append(buf, '}')
}

// CredentialEntry is the decoded merge input for one GlobalPullCredential.
type CredentialEntry struct {
	Name     string
	Hostname string
	Username string
	Password string
}

// credentialStatus is the reconcile result reported for one CR.
type credentialStatus struct {
	Applied bool
	Reason  string
	Message string
}

// MergeResult contains the rendered Secret and each CR's reconcile result.
type MergeResult struct {
	MergedJSON []byte
	Statuses   map[string]credentialStatus
}

// MergeCredentials merges CR credentials into the baseline pull secret. The
// baseline always wins, and the result preserves its non-auths fields.
func MergeCredentials(baseline []byte, entries []CredentialEntry) (*MergeResult, error) {
	var parsed dockerConfigJSON
	if err := json.Unmarshal(baseline, &parsed); err != nil {
		return nil, fmt.Errorf("parsing baseline pull-secret: %w", err)
	}

	seen := make(map[string]string, len(entries))
	for _, entry := range entries {
		if owner, exists := seen[entry.Hostname]; exists {
			return nil, fmt.Errorf("duplicate hostname %q claimed by %q and %q", entry.Hostname, owner, entry.Name)
		}
		seen[entry.Hostname] = entry.Name
	}

	auths := make(map[string]json.RawMessage, len(parsed.Auths)+len(entries))
	for host, auth := range parsed.Auths {
		auths[host] = auth
	}

	result := &MergeResult{Statuses: make(map[string]credentialStatus, len(entries))}
	for _, entry := range entries {
		if _, onBaseline := parsed.Auths[entry.Hostname]; onBaseline {
			result.Statuses[entry.Name] = credentialStatus{
				Reason:  baselinePrecedenceReason,
				Message: fmt.Sprintf("baseline pull-secret credential for %q is used instead", entry.Hostname),
			}
			continue
		}

		auth, err := json.Marshal(map[string]string{"auth": encodeAuth(entry.Username, entry.Password)})
		if err != nil {
			return nil, fmt.Errorf("encoding auth for %q: %w", entry.Hostname, err)
		}
		auths[entry.Hostname] = auth
		result.Statuses[entry.Name] = credentialStatus{
			Applied: true,
			Reason:  mergedReason,
			Message: fmt.Sprintf("credential for %q merged into pull-secret-merged", entry.Hostname),
		}
	}

	mergedJSON, err := json.Marshal(dockerConfigJSON{Auths: auths, Extra: parsed.Extra})
	if err != nil {
		return nil, fmt.Errorf("marshaling merged pull-secret: %w", err)
	}
	result.MergedJSON = mergedJSON
	return result, nil
}

// encodeAuth returns the Docker config.json auth value,
// base64("username:password").
func encodeAuth(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}
