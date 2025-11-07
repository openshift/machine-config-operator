package extended

// ign32PaswdUser represents a user in Ignition 3.2 format
type ign32PaswdUser struct {
	Name              string   `json:"name,omitempty"`
	SSHAuthorizedKeys []string `json:"sshAuthorizedKeys,omitempty"`
	PasswordHash      string   `json:"passwordHash,omitempty"`
}
