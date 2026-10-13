// Copyright 2020 Red Hat, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package v35tov34

import (
	"fmt"
	"reflect"

	"github.com/coreos/ignition/v2/config/translate"
	"github.com/coreos/ignition/v2/config/v3_4/types"

	old_types "github.com/coreos/ignition/v2/config/v3_5/types"
	"github.com/coreos/ignition/v2/config/validate"
)

// Copy of github.com/coreos/ignition/v2/config/v3_5/translate/translate.go
// with the types & old_types imports reversed (the referenced file translates
// from 3.5 -> 3.4 but as a result only touches fields that are understood by
// the 3.4 spec).
func translateIgnition(old old_types.Ignition) (ret types.Ignition) {
	// use a new translator so we don't recurse infinitely
	translate.NewTranslator().Translate(&old, &ret)
	ret.Version = types.MaxVersion.String()
	return
}
func translateLuks(old old_types.Luks) (ret types.Luks) {
	tr := translate.NewTranslator()
	tr.AddCustomTranslator(translateTang)
	tr.Translate(&old.Clevis, &ret.Clevis)
	tr.Translate(&old.Device, &ret.Device)
	tr.Translate(&old.KeyFile, &ret.KeyFile)
	tr.Translate(&old.Label, &ret.Label)
	tr.Translate(&old.Name, &ret.Name)
	tr.Translate(&old.OpenOptions, &ret.OpenOptions)
	tr.Translate(&old.Options, &ret.Options)
	tr.Translate(&old.Discard, &ret.Discard)
	tr.Translate(&old.UUID, &ret.UUID)
	tr.Translate(&old.WipeVolume, &ret.WipeVolume)
	return
}
func translateTang(old old_types.Tang) (ret types.Tang) {
	tr := translate.NewTranslator()
	tr.Translate(&old.Thumbprint, &ret.Thumbprint)
	tr.Translate(&old.URL, &ret.URL)
	return
}
func translateConfig(old old_types.Config) (ret types.Config) {
	tr := translate.NewTranslator()
	tr.AddCustomTranslator(translateIgnition)
	tr.AddCustomTranslator(translateLuks)
	tr.Translate(&old, &ret)
	return
}

// end copied Ignition v3_5/translate block

// Translate translates Ignition spec config v3.5 to spec v3.4
func Translate(cfg old_types.Config) (types.Config, error) {
	rpt := validate.ValidateWithContext(cfg, nil)
	if rpt.IsFatal() {
		return types.Config{}, fmt.Errorf("invalid input config:\n%s", rpt.String())
	}

	err := checkValue(reflect.ValueOf(cfg))
	if err != nil {
		return types.Config{}, err
	}

	res := translateConfig(cfg)

	// Sanity check the returned config
	oldrpt := validate.ValidateWithContext(res, nil)
	if oldrpt.IsFatal() {
		return types.Config{}, fmt.Errorf("converted spec has unexpected fatal error:\n%s", oldrpt.String())
	}
	return res, nil
}

func checkValue(v reflect.Value) error {
	// v3.5 introduced Cex type
	if v.Type() == reflect.TypeOf(old_types.Cex{}) {
		return fmt.Errorf("invalid input config: 'Cex' type is not supported in spec v3.4")
	}

	return nil
}
