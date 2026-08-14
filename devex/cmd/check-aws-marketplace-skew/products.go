package main

import (
	"sort"
	"strings"

	bootimagemarketplace "github.com/openshift/machine-config-operator/pkg/controller/bootimage/marketplace"
)

// ProductSpec is a Marketplace product entry paired with the architecture whose installer
// ceiling it should be checked against.
type ProductSpec struct {
	ID, Name, Arch string
}

// archSortWeight orders x86_64 before arm64 in report output; alphabetical order would put arm64
// first ('a' < 'x'), which reads backwards from how these are conventionally listed.
func archSortWeight(arch string) int {
	if arch == "arm64" {
		return 1
	}
	return 0
}

// allProductSpecs derives arch for each shared-package product entry by inspecting its name.
// Every entry encodes its architecture in the name. Results are ordered by arch, then region
// (standard before EMEA), then name, so the report table reads as grouped sections instead of
// shuffled by the underlying (arbitrary) product UUID.
//
// ROSA Classic is excluded: it's being sunset, so its Marketplace image freshness is no longer
// worth tracking here. marketplace.ROSAProductID/Products["ROSA"] stay in the shared package
// as-is — the boot image controller still needs them for AMI-kind detection on existing clusters.
func allProductSpecs() []ProductSpec {
	specs := make([]ProductSpec, 0, len(bootimagemarketplace.Products))
	for id, name := range bootimagemarketplace.Products {
		if id == bootimagemarketplace.ROSAProductID {
			continue
		}
		arch := "x86_64"
		if strings.Contains(name, "arm64") {
			arch = "arm64"
		}
		specs = append(specs, ProductSpec{ID: id, Name: name, Arch: arch})
	}
	sort.Slice(specs, func(i, j int) bool {
		a, b := specs[i], specs[j]
		if wa, wb := archSortWeight(a.Arch), archSortWeight(b.Arch); wa != wb {
			return wa < wb
		}
		if aEMEA, bEMEA := strings.Contains(a.Name, "EMEA"), strings.Contains(b.Name, "EMEA"); aEMEA != bEMEA {
			return !aEMEA
		}
		return a.Name < b.Name
	})
	return specs
}
