// Copyright 2020 Red Hat, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v23tov30

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"

	old "github.com/coreos/ignition/config/v2_3/types"
	oldValidate "github.com/coreos/ignition/config/validate"
	"github.com/coreos/ignition/v2/config/v3_0/types"
	"github.com/coreos/ignition/v2/config/validate"

	"github.com/coreos/ign-converter/util"
)

// Check2_3 returns if the config is translatable but does not do any translation.
// fsMap is a map from v2 filesystem names to the paths under which they should
// be mounted in v3.
func Check2_3(cfg old.Config, fsMap map[string]string) error {
	rpt := oldValidate.ValidateWithoutSource(reflect.ValueOf(cfg))
	if rpt.IsFatal() || rpt.IsDeprecated() {
		// disallow any deprecated fields
		return fmt.Errorf("Invalid input config:\n%s", rpt.String())
	}

	if len(cfg.Networkd.Units) != 0 {
		return util.UsesNetworkdError
	}

	// check that all filesystems have a path
	if fsMap == nil {
		fsMap = map[string]string{}
	}
	fsMap["root"] = "/"
	for _, fs := range cfg.Storage.Filesystems {
		if _, ok := fsMap[fs.Name]; !ok {
			return util.NoFilesystemError(fs.Name)
		}
	}

	// check that there are no duplicates with files, links, or directories
	// from path to a pretty-printing description of the entry
	entryMap := map[string]string{}
	links := make([]string, 0, len(cfg.Storage.Links))
	// build up a list of all the links we write. We're not allow to use links
	// that we write
	for _, link := range cfg.Storage.Links {
		path := filepath.Join("/", fsMap[link.Filesystem], link.Path)
		links = append(links, path)
	}

	for _, file := range cfg.Storage.Files {
		path := filepath.Join("/", fsMap[file.Filesystem], file.Path)
		name := fmt.Sprintf("File: %s", path)
		if duplicate, isDup := entryMap[path]; isDup {
			return util.DuplicateInodeError{duplicate, name}
		}
		if l := util.CheckPathUsesLink(links, path); l != "" {
			return &util.UsesOwnLinkError{
				LinkPath: l,
				Name:     name,
			}
		}
		entryMap[path] = name
	}
	for _, dir := range cfg.Storage.Directories {
		path := filepath.Join("/", fsMap[dir.Filesystem], dir.Path)
		name := fmt.Sprintf("Directory: %s", path)
		if duplicate, isDup := entryMap[path]; isDup {
			return util.DuplicateInodeError{duplicate, name}
		}
		if l := util.CheckPathUsesLink(links, path); l != "" {
			return &util.UsesOwnLinkError{
				LinkPath: l,
				Name:     name,
			}
		}
		entryMap[path] = name
	}
	for _, link := range cfg.Storage.Links {
		path := filepath.Join("/", fsMap[link.Filesystem], link.Path)
		name := fmt.Sprintf("Link: %s", path)
		if duplicate, isDup := entryMap[path]; isDup {
			return &util.DuplicateInodeError{duplicate, name}
		}
		entryMap[path] = name
		if l := util.CheckPathUsesLink(links, path); l != "" {
			return &util.UsesOwnLinkError{
				LinkPath: l,
				Name:     name,
			}
		}
	}
	return nil
}

// Translate translates spec v2.3 to v3.0
func Translate(cfg old.Config, fsMap map[string]string) (types.Config, error) {
	if err := Check2_3(cfg, fsMap); err != nil {
		return types.Config{}, err
	}
	res := types.Config{
		// Ignition section
		Ignition: types.Ignition{
			Version: "3.0.0",
			Config: types.IgnitionConfig{
				Replace: translateCfgRef(cfg.Ignition.Config.Replace),
				Merge:   translateCfgRefs(cfg.Ignition.Config.Append),
			},
			Security: types.Security{
				TLS: types.TLS{
					CertificateAuthorities: translateCAs(cfg.Ignition.Security.TLS.CertificateAuthorities),
				},
			},
			Timeouts: types.Timeouts{
				HTTPResponseHeaders: cfg.Ignition.Timeouts.HTTPResponseHeaders,
				HTTPTotal:           cfg.Ignition.Timeouts.HTTPTotal,
			},
		},
		// Passwd section
		Passwd: types.Passwd{
			Users:  translateUsers(cfg.Passwd.Users),
			Groups: translateGroups(cfg.Passwd.Groups),
		},
		Systemd: types.Systemd{
			Units: translateUnits(cfg.Systemd.Units),
		},
		Storage: types.Storage{
			Disks:       translateDisks(cfg.Storage.Disks),
			Raid:        translateRaid(cfg.Storage.Raid),
			Filesystems: translateFilesystems(cfg.Storage.Filesystems, fsMap),
			Files:       translateFiles(cfg.Storage.Files, fsMap),
			Directories: translateDirectories(cfg.Storage.Directories, fsMap),
			Links:       translateLinks(cfg.Storage.Links, fsMap),
		},
	}
	r := validate.ValidateWithContext(res, nil)
	if r.IsFatal() {
		return types.Config{}, errors.New(r.String())
	}
	return res, nil
}

func translateCfgRef(ref *old.ConfigReference) (ret types.ConfigReference) {
	if ref == nil {
		return
	}
	ret.Source = &ref.Source
	ret.Verification.Hash = ref.Verification.Hash
	return
}

func translateCfgRefs(refs []old.ConfigReference) (ret []types.ConfigReference) {
	for _, ref := range refs {
		ret = append(ret, translateCfgRef(&ref))
	}
	return
}

func translateCAs(refs []old.CaReference) (ret []types.CaReference) {
	for _, ref := range refs {
		ret = append(ret, types.CaReference{
			Source: ref.Source,
			Verification: types.Verification{
				Hash: ref.Verification.Hash,
			},
		})
	}
	return
}

func translateUsers(users []old.PasswdUser) (ret []types.PasswdUser) {
	for _, u := range users {
		ret = append(ret, types.PasswdUser{
			Name:              u.Name,
			PasswordHash:      u.PasswordHash,
			SSHAuthorizedKeys: translateUserSSH(u.SSHAuthorizedKeys),
			UID:               u.UID,
			Gecos:             util.StrP(u.Gecos),
			HomeDir:           util.StrP(u.HomeDir),
			NoCreateHome:      util.BoolP(u.NoCreateHome),
			PrimaryGroup:      util.StrP(u.PrimaryGroup),
			Groups:            translateUserGroups(u.Groups),
			NoUserGroup:       util.BoolP(u.NoUserGroup),
			NoLogInit:         util.BoolP(u.NoLogInit),
			Shell:             util.StrP(u.Shell),
			System:            util.BoolP(u.System),
		})
	}
	return
}

func translateUserSSH(in []old.SSHAuthorizedKey) (ret []types.SSHAuthorizedKey) {
	for _, k := range in {
		ret = append(ret, types.SSHAuthorizedKey(k))
	}
	return
}

func translateUserGroups(in []old.Group) (ret []types.Group) {
	for _, g := range in {
		ret = append(ret, types.Group(g))
	}
	return
}

func translateGroups(groups []old.PasswdGroup) (ret []types.PasswdGroup) {
	for _, g := range groups {
		ret = append(ret, types.PasswdGroup{
			Name:         g.Name,
			Gid:          g.Gid,
			PasswordHash: util.StrP(g.PasswordHash),
			System:       util.BoolP(g.System),
		})
	}
	return
}

func translateUnits(units []old.Unit) (ret []types.Unit) {
	for _, u := range units {
		var enabled *bool
		// The Enabled field wins over Enable, since Enable is deprecated in spec v2 and removed in v3.
		// It does so following the apparent intent of the upstream code [1]
		// which actually does the opposite for Enable=true Enabled=false
		// because the first matching line in a systemd preset wins.
		// [1] https://github.com/coreos/ignition/blob/b4d18ad3fcb278a890327f858c1c10256ab6ee9d/internal/exec/stages/files/units.go#L32
		if (u.Enabled != nil && *u.Enabled) || u.Enable {
			enabled = util.BoolP(true)
		}
		if u.Enabled != nil && !*u.Enabled {
			enabled = util.BoolPStrict(false)
		}
		ret = append(ret, types.Unit{
			Name:     u.Name,
			Enabled:  enabled,
			Mask:     util.BoolP(u.Mask),
			Contents: util.StrP(u.Contents),
			Dropins:  translateDropins(u.Dropins),
		})
	}
	return
}

func translateDropins(dropins []old.SystemdDropin) (ret []types.Dropin) {
	for _, d := range dropins {
		ret = append(ret, types.Dropin{
			Name:     d.Name,
			Contents: util.StrP(d.Contents),
		})
	}
	return
}

func translateDisks(disks []old.Disk) (ret []types.Disk) {
	for _, d := range disks {
		ret = append(ret, types.Disk{
			Device:     d.Device,
			WipeTable:  util.BoolP(d.WipeTable),
			Partitions: translatePartitions(d.Partitions),
		})
	}
	return
}

func translatePartitions(parts []old.Partition) (ret []types.Partition) {
	for _, p := range parts {
		ret = append(ret, types.Partition{
			Label:              p.Label,
			Number:             p.Number,
			SizeMiB:            p.SizeMiB,
			StartMiB:           p.StartMiB,
			TypeGUID:           util.StrP(p.TypeGUID),
			GUID:               util.StrP(p.GUID),
			WipePartitionEntry: util.BoolP(p.WipePartitionEntry),
			ShouldExist:        p.ShouldExist,
		})
	}
	return
}

func translateRaid(raids []old.Raid) (ret []types.Raid) {
	for _, r := range raids {
		ret = append(ret, types.Raid{
			Name:    r.Name,
			Level:   r.Level,
			Devices: translateDevices(r.Devices),
			Spares:  util.IntP(r.Spares),
			Options: translateRaidOptions(r.Options),
		})
	}
	return
}

func translateDevices(devices []old.Device) (ret []types.Device) {
	for _, d := range devices {
		ret = append(ret, types.Device(d))
	}
	return
}

func translateRaidOptions(options []old.RaidOption) (ret []types.RaidOption) {
	for _, o := range options {
		ret = append(ret, types.RaidOption(o))
	}
	return
}

func translateFilesystems(fss []old.Filesystem, m map[string]string) (ret []types.Filesystem) {
	for _, f := range fss {
		if f.Name == "root" {
			// root is implied
			continue
		}
		if f.Mount == nil {
			f.Mount = &old.Mount{}
		}
		ret = append(ret, types.Filesystem{
			Device:         f.Mount.Device,
			Format:         util.StrP(f.Mount.Format),
			WipeFilesystem: util.BoolP(f.Mount.WipeFilesystem),
			Label:          f.Mount.Label,
			UUID:           f.Mount.UUID,
			Options:        translateFilesystemOptions(f.Mount.Options),
			Path:           util.StrP(m[f.Name]),
		})
	}
	return
}

func translateFilesystemOptions(options []old.MountOption) (ret []types.FilesystemOption) {
	for _, o := range options {
		ret = append(ret, types.FilesystemOption(o))
	}
	return
}

func translateNode(n old.Node, m map[string]string) types.Node {
	if n.User == nil {
		n.User = &old.NodeUser{}
	}
	if n.Group == nil {
		n.Group = &old.NodeGroup{}
	}
	return types.Node{
		Path: filepath.Join(m[n.Filesystem], n.Path),
		User: types.NodeUser{
			ID:   n.User.ID,
			Name: util.StrP(n.User.Name),
		},
		Group: types.NodeGroup{
			ID:   n.Group.ID,
			Name: util.StrP(n.Group.Name),
		},
		Overwrite: n.Overwrite,
	}
}

func translateFiles(files []old.File, m map[string]string) (ret []types.File) {
	for _, f := range files {
		// 2.x files are overwrite by default
		if f.Node.Overwrite == nil {
			f.Node.Overwrite = util.BoolP(true)
		}

		// In spec 3, overwrite must be false if append is true
		// i.e. spec 2 files with append true must be translated to spec 3 files with overwrite false
		if f.FileEmbedded1.Append == true {
			f.Node.Overwrite = util.BoolPStrict(false)
		}

		file := types.File{
			Node: translateNode(f.Node, m),
			FileEmbedded1: types.FileEmbedded1{
				Mode: f.Mode,
			},
		}
		c := types.FileContents{
			Compression: util.StrP(f.Contents.Compression),
			Source:      util.StrPStrict(f.Contents.Source),
		}
		c.Verification.Hash = f.FileEmbedded1.Contents.Verification.Hash

		if f.Append {
			file.Append = []types.FileContents{c}
		} else {
			file.Contents = c
		}
		ret = append(ret, file)
	}
	return
}

func translateLinks(links []old.Link, m map[string]string) (ret []types.Link) {
	for _, l := range links {
		ret = append(ret, types.Link{
			Node: translateNode(l.Node, m),
			LinkEmbedded1: types.LinkEmbedded1{
				Hard:   util.BoolP(l.Hard),
				Target: l.Target,
			},
		})
	}
	return
}

func translateDirectories(dirs []old.Directory, m map[string]string) (ret []types.Directory) {
	for _, d := range dirs {
		ret = append(ret, types.Directory{
			Node: translateNode(d.Node, m),
			DirectoryEmbedded1: types.DirectoryEmbedded1{
				Mode: d.Mode,
			},
		})
	}
	return
}
