package extended

// Ignition 2.2.0
// ign22FileUser describes the user that will own a given file
type ign22FileUser struct {
	Name string `json:"name,omitempty"`
	ID   *int   `json:"id,omitempty"`
}

// ign22FileGroup describes the group that will own a given file
type ign22FileGroup struct {
	Name string `json:"name,omitempty"`
	ID   *int   `json:"id,omitempty"`
}

// ign22Contents describes the "contents" field in an ignition 2.2.0 File configuration
type ign22Contents struct {
	Compression string `json:"compression,omitempty"`
	Source      string `json:"source,omitempty"`
}

// ign22File describes the configuration of a File in ignition 2.2.0
type ign22File struct {
	Path       string          `json:"path,omitempty"`
	Contents   ign22Contents   `json:"contents,omitempty"`
	Mode       *int            `json:"mode,omitempty"`
	User       *ign22FileUser  `json:"user,omitempty"`
	Group      *ign22FileGroup `json:"group,omitempty"`
	Filesystem string          `json:"filesystem,omitempty"`
}

// Ignition 3.2.0.
// ign32Contents describes the "contents" field in an ignition 3.2.0 File configuration
type ign32Contents struct {
	Compression string `json:"compression,omitempty"`
	Source      string `json:"source,omitempty"`
}

// ign32FileUser describes the user that will own a given file
type ign32FileUser struct {
	Name string `json:"name,omitempty"`
	ID   *int   `json:"id,omitempty"`
}

// ign32FileGroup describes the group that will own a given file
type ign32FileGroup struct {
	Name string `json:"name,omitempty"`
	ID   *int   `json:"id,omitempty"`
}

// ign32File describes the configuration of a File in ignition 3.0.0, 3.1.0, 3.2.0, 3.3.0 and 3.4.0
type ign32File struct {
	Path     string          `json:"path,omitempty"`
	Contents ign32Contents   `json:"contents,omitempty"`
	Mode     *int            `json:"mode,omitempty"`
	User     *ign32FileUser  `json:"user,omitempty"`
	Group    *ign32FileGroup `json:"group,omitempty"`
}

// ign32PaswdUser represents a user in Ignition 3.2 format
type ign32PaswdUser struct {
	Name              string   `json:"name,omitempty"`
	SSHAuthorizedKeys []string `json:"sshAuthorizedKeys,omitempty"`
	PasswordHash      string   `json:"passwordHash,omitempty"`
}
