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
package v36tov35

import (
	"fmt"
	"reflect"

	"github.com/coreos/ignition/v2/config/translate"
	"github.com/coreos/ignition/v2/config/util"
	"github.com/coreos/ignition/v2/config/v3_5/types"
	old_types "github.com/coreos/ignition/v2/config/v3_6/types"
	"github.com/coreos/ignition/v2/config/validate"
)

// Copy of github.com/coreos/ignition/v2/config/v3_5/translate/translate.go
// with the types & old_types imports reversed (the referenced file translates
// from 3.5 -> 3.6 but as a result only touches fields that are understood by
// the 3.5 spec).
func translateIgnition(old old_types.Ignition) (ret types.Ignition) {
	// use a new translator so we don't recurse infinitely
	translate.NewTranslator().Translate(&old, &ret)
	ret.Version = types.MaxVersion.String()
	return
}

func translateConfig(old old_types.Config) (ret types.Config) {
	tr := translate.NewTranslator()
	tr.AddCustomTranslator(translateIgnition)
	tr.Translate(&old, &ret)
	return
}

// end copied Ignition v3_6/translate block

// Translate translates Ignition spec config v3.6 to spec v3.5
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
	// v3.6 introduced arbitrary custom clevis pin support
	if v.Type() == reflect.TypeOf(old_types.ClevisCustom{}) {
		// Check if Pin field is set
		pinField := v.FieldByName("Pin")
		if pinField.IsValid() && !pinField.IsNil() {
			pinValue := pinField.Elem().String()
			// v3.5 only supports tpm2, tang, and sss
			if pinValue != "tpm2" && pinValue != "tang" && pinValue != "sss" {
				return fmt.Errorf("invalid input config: arbitrary custom clevis pin '%s' is not supported in spec v3.5", pinValue)
			}
		}
	}
	// v3.6 supports special mode bits, but v3.5 and earlier have broken support.
	// Fail translation to avoid silently breaking working functionality.
	if v.Type() == reflect.TypeOf(old_types.FileEmbedded1{}) {
		f := v.Interface().(old_types.FileEmbedded1)
		if f.Mode != nil && (*f.Mode&07000) != 0 {
			return fmt.Errorf("invalid input config: special mode bits are not supported in spec v3.5")
		}
	}
	if v.Type() == reflect.TypeOf(old_types.DirectoryEmbedded1{}) {
		d := v.Interface().(old_types.DirectoryEmbedded1)
		if d.Mode != nil && (*d.Mode&07000) != 0 {
			return fmt.Errorf("invalid input config: special mode bits are not supported in spec v3.5")
		}
	}

	return descend(v)
}

func descend(v reflect.Value) error {
	k := v.Type().Kind()
	switch {
	case util.IsPrimitive(k):
		return nil
	case k == reflect.Struct:
		for i := 0; i < v.NumField(); i += 1 {
			err := checkValue(v.Field(i))
			if err != nil {
				return err
			}
		}
	case k == reflect.Slice:
		for i := 0; i < v.Len(); i += 1 {
			err := checkValue(v.Index(i))
			if err != nil {
				return err
			}
		}
	case k == reflect.Ptr:
		v = v.Elem()
		if v.IsValid() {
			return checkValue(v)
		}
	}
	return nil
}
