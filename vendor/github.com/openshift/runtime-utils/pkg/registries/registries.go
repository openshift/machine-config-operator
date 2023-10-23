package registries

import (
	"fmt"
	"sort"
	"strings"

	"github.com/containers/image/v5/pkg/sysregistriesv2"
	apicfgv1 "github.com/openshift/api/config/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
)

// ScopeIsNestedInsideScope returns true if a subScope value (as in sysregistriesv2.Registry.Prefix / sysregistriesv2.Endpoint.Location)
// is a sub-scope of superScope.
func ScopeIsNestedInsideScope(subScope, superScope string) bool {
	match := false
	if superScope == subScope {
		return true
	}
	// return true if subScope defines a namespace/repo inside (non-wildcard) superScope
	if len(subScope) > len(superScope) && strings.HasPrefix(subScope, superScope) && subScope[len(superScope)] == '/' {
		return true
	}
	// return true if scope is a value that is a sub-scope of reg
	// e.g *.foo.example.com is a sub-scope of *.example.com or bar.example.com/bar is a sub-scope of *.example.com
	// and check that we are not matching on namespace or repo e.g *.foo should not match quay/bar.foo or quay/bar.foo/example or quay/bar.foo:400
	if strings.HasPrefix(superScope, "*.") {
		if strings.Contains(subScope, ":") {
			arr := strings.Split(subScope, ":")
			match = strings.HasSuffix(arr[0], superScope[1:]) && !strings.Contains(arr[0], "/")
		} else {
			arr := strings.Split(subScope, "/")
			match = strings.HasSuffix(arr[0], superScope[1:])
		}
	}
	return match
}

// mirrorsContainsARealMirror returns true if mirrors contains at least one entry that is not source.
func mirrorsContainsARealMirror(source string, mirrors []apicfgv1.ImageMirror) bool {
	for _, mirror := range mirrors {
		if string(mirror) != source {
			return true
		}
	}
	return false
}

// mirrorSet collects data from mirror setting CRDs (ImageDigestMirrorSet, ImageTagMirrorSet)
type mirrorSets struct {
	disjointSets      map[string]*[][]string // Key == Source
	mirrorBlockSource map[string]bool        // key == Source
}

func newMirrorSets() *mirrorSets {
	return &mirrorSets{
		disjointSets:      map[string]*[][]string{},
		mirrorBlockSource: map[string]bool{},
	}
}

func (sets *mirrorSets) addMirrorSet(source string, mirrorSourcePolicy apicfgv1.MirrorSourcePolicy, mirrors []apicfgv1.ImageMirror) {
	if !mirrorsContainsARealMirror(source, mirrors) {
		return // No mirrors (or mirrors that only repeat the authoritative source) is not really a mirror set. Ignore mirrorSourcePolicy intentionally.
	}
	strMirrors := []string{}
	for _, m := range mirrors {
		strMirrors = append(strMirrors, (string(m)))
	}
	if mirrorSourcePolicy == apicfgv1.NeverContactSource {
		sets.mirrorBlockSource[source] = true
	}
	ds, ok := sets.disjointSets[source]
	if !ok {
		ds = &[][]string{}
		sets.disjointSets[source] = ds
	}
	*ds = append(*ds, strMirrors)
}

// mergedMirrors generates deterministic order of mirrors for a given source
func (sets *mirrorSets) mergedMirrors(source string) ([]string, error) {
	topoGraph := newTopoGraph()
	for _, mirrors := range *sets.disjointSets[source] {
		for i := 0; i+1 < len(mirrors); i++ {
			topoGraph.AddEdge(mirrors[i], mirrors[i+1])
		}
		sourceInGraph := false
		for _, m := range mirrors {
			if m == source {
				sourceInGraph = true
				break
			}
		}
		if !sourceInGraph {
			// mirrorSets.addMirrorSet guarantees len(set.Mirrors) > 0.
			topoGraph.AddEdge(mirrors[len(mirrors)-1], source)
		}
		// Every node in topoGraph, including source, is implicitly added by topoGraph.AddEdge (every mirror set contains at least one non-source mirror,
		// so there are no unconnected nodes that we would have to add separately from the edges).
	}
	sortedRepos, err := topoGraph.Sorted()
	if err != nil {
		return nil, err
	}
	if sortedRepos[len(sortedRepos)-1] == source {
		// We don't need to explicitly include source in the list, it will be automatically tried last per the semantics of sysregistriesv2. Mirrors.
		sortedRepos = sortedRepos[:len(sortedRepos)-1]
	}

	if err != nil {
		return nil, err
	}
	return sortedRepos, nil
}

type mergedMirrorSet struct {
	source             string
	mirrors            []string
	mirrorSourcePolicy apicfgv1.MirrorSourcePolicy
}

// mergedMirrorSets converts the set of mirrors to slice of mergedMirrorSet
func mergedMirrorSets(sets *mirrorSets) ([]mergedMirrorSet, error) {
	// Sort the sets of mirrors by Source to ensure deterministic output
	sources := []string{}
	for key := range sets.disjointSets {
		sources = append(sources, key)
	}
	// collects the mirror sources and sorted in increasing order
	sort.Strings(sources)
	// Convert the sets of mirrors
	res := []mergedMirrorSet{}
	for _, source := range sources {
		mirrors, err := sets.mergedMirrors(source)
		if err != nil {
			return nil, err
		}
		item := mergedMirrorSet{
			source:  source,
			mirrors: mirrors,
		}
		if sets.mirrorBlockSource[source] {
			item.mirrorSourcePolicy = apicfgv1.NeverContactSource
		}
		res = append(res, item)
	}
	return res, nil
}

// mergedTagMirrorSets processes itmsRules and returns a set of mergedMirrorSet, one for each Source value,
// ordered consistently with the preference order of the individual entries (if possible)
// E.g. given mirror sets (B, C) and (A, B), it will combine them into a single (A, B, C) set.
func mergedTagMirrorSets(itmsRules []*apicfgv1.ImageTagMirrorSet) ([]mergedMirrorSet, error) {
	tagMirrorSets := newMirrorSets()
	for _, itms := range itmsRules {
		for _, set := range itms.Spec.ImageTagMirrors {
			tagMirrorSets.addMirrorSet(set.Source, set.MirrorSourcePolicy, set.Mirrors)
		}
	}
	return mergedMirrorSets(tagMirrorSets)
}

// mergedDigestMirrorSets processes idmsRules and icspRules and returns a set of mergedMirrorSet, one for each Source value,
// ordered consistently with the preference order of the individual entries (if possible)
// E.g. given mirror sets (B, C) and (A, B), it will combine them into a single (A, B, C) set.
func mergedDigestMirrorSets(idmsRules []*apicfgv1.ImageDigestMirrorSet, icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy) ([]mergedMirrorSet, error) {
	mirrorSets := newMirrorSets()
	for _, idms := range idmsRules {
		for _, set := range idms.Spec.ImageDigestMirrors {
			mirrorSets.addMirrorSet(set.Source, set.MirrorSourcePolicy, set.Mirrors)
		}
	}
	for _, icsp := range icspRules {
		for _, set := range icsp.Spec.RepositoryDigestMirrors {
			imgMirrors := []apicfgv1.ImageMirror{}
			for _, m := range set.Mirrors {
				imgMirrors = append(imgMirrors, apicfgv1.ImageMirror(m))
			}
			// leave MirrorSourcePolicy blank, it will follow the default AllowContactingSource
			mirrorSets.addMirrorSet(set.Source, "", imgMirrors)
		}
	}
	return mergedMirrorSets(mirrorSets)
}

// mirrorsAdjustedForNestedScope returns mirrors from mirroredScope, updated
// so that they can be configured in a nested subScope, without any change in the
// semantics of the mirrors.
func mirrorsAdjustedForNestedScope(mirroredScope, subScope string, mirrors []sysregistriesv2.Endpoint) ([]sysregistriesv2.Endpoint, error) {
	// Sanity checks, just to be sure.
	if !ScopeIsNestedInsideScope(subScope, mirroredScope) {
		return nil, fmt.Errorf("internal error: mirrorsAdjustedForNestedScope for %#v and non-subscope %#v", mirroredScope, subScope)
	}
	if strings.HasPrefix(mirroredScope, "*.") {
		return nil, fmt.Errorf("internal error: mirrorsAdjustedForNestedScope for a wildcard scope %#v", mirroredScope)
	}
	// If mirorredScope is not a wildcard, ScopeIsNestedInsideScope ensures that subScope is not a wildcard either
	// So, both scopes should be simple namespaces, and ScopeIsNestedInsideScope should guarantee this.
	if !strings.HasPrefix(subScope, mirroredScope) {
		return nil, fmt.Errorf("internal error: mirrorsAdjustedForNestedScope with unexpected scopes %#v and %#v", mirroredScope, subScope)
	}
	adjustment := subScope[len(mirroredScope):]
	res := []sysregistriesv2.Endpoint{}
	for _, original := range mirrors {
		updated := original
		updated.Location = updated.Location + adjustment
		res = append(res, updated)
	}
	return res, nil
}

// registryScope returns the scope used for matching a registry entry.
// (Eventually https://github.com/containers/image/pull/1368 should allow us to only set Prefix
// entries, and this function will be unnecessary.)
func registryScope(reg *sysregistriesv2.Registry) string {
	if reg.Prefix != "" {
		return reg.Prefix
	}
	return reg.Location
}

// EditRegistriesConfig edits, IN PLACE, the /etc/containers/registries.conf configuration provided in config, to:
// - Mark scope entries in insecureScopes as insecure (TLS is not required, and TLS certificate verification is not required when TLS is used)
// - Mark scope entries in blockedScopes as blocked (any attempts to access them fail)
// - Implement ImageContentSourcePolicy rules in icspRules.
// - Implement ImageDigestMirrorSet rules in idmsRules.
// - Implement ImageTagMirrorSet rules in itmsRules.
// "scopes" can be any of whole registries, which means that the configuration applies to everything on that registry, including any possible separately-configured
// namespaces/repositories within that registry.
// or can be wildcard entries, which means that we accept wildcards in the form of *.example.registry.com for insecure and blocked registries only. We do not
// accept them for mirror configuration.
// A valid scope is in the form of registry/namespace...[/repo] (can also refer to sysregistriesv2.Registry.Prefix)
// NOTE: Validation of wildcard entries is done before EditRegistriesConfig is called in the MCO code.
func EditRegistriesConfig(config *sysregistriesv2.V2RegistriesConf, insecureScopes, blockedScopes []string, icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy,
	idmsRules []*apicfgv1.ImageDigestMirrorSet, itmsRules []*apicfgv1.ImageTagMirrorSet,
) error {
	// addRegistryEntry creates a Registry object corresponding to scope.
	// NOTE: The pointer is valid only until the next getRegistryEntry call.
	addRegistryEntry := func(scope string) *sysregistriesv2.Registry {
		// If scope is a wildcard entry, add it to the registry Prefix
		reg := sysregistriesv2.Registry{}
		if strings.HasPrefix(scope, "*.") {
			reg.Prefix = scope
			// Otherwise it is a regular entry so add it to the registry endpoint Location
		} else {
			reg.Location = scope
		}
		config.Registries = append(config.Registries, reg)
		return &config.Registries[len(config.Registries)-1]
	}

	// getRegistryEntry returns a pointer to a modifiable Registry object corresponding to scope,
	// creating it if necessary.
	// If Prefix doesn't have a wildcard entry, we check Location for regular entries.
	// NOTE: The pointer is valid only until the next getRegistryEntry call.
	getRegistryEntry := func(scope string) *sysregistriesv2.Registry {
		for i := range config.Registries {
			reg := &config.Registries[i]
			if registryScope(reg) == scope {
				return reg
			}
		}
		return addRegistryEntry(scope)
	}

	addMirrorsToRegistries := func(mergedMirrorSets []mergedMirrorSet, pullFromMirror string) {
		for _, mirrorItem := range mergedMirrorSets {
			reg := getRegistryEntry(mirrorItem.source)
			if mirrorItem.mirrorSourcePolicy == apicfgv1.NeverContactSource {
				reg.Blocked = true
			}
			for _, mirror := range mirrorItem.mirrors {
				reg.Mirrors = append(reg.Mirrors, sysregistriesv2.Endpoint{Location: mirror, PullFromMirror: pullFromMirror})
			}
		}
	}

	digestMirrorSets, err := mergedDigestMirrorSets(idmsRules, icspRules)
	if err != nil {
		return err
	}
	addMirrorsToRegistries(digestMirrorSets, sysregistriesv2.MirrorByDigestOnly)

	tagMirrorSets, err := mergedTagMirrorSets(itmsRules)
	if err != nil {
		return err
	}
	addMirrorsToRegistries(tagMirrorSets, sysregistriesv2.MirrorByTagOnly)

	// Add the blocked registry entries to the registries list so that we can find sub-scopes of insecure registries and set both the
	// blocked and insecure flags accordingly.
	// e.g *.blocked.insecure.com is a sub-scope of *.insecure.com and should have both the insecure and blocked options set to true. If
	// we don't add the blocked registries list to the registries config list before going through the insecure registries we won't be able
	// to check if *.blocked.insecure.com is a sub-scope of *.insecure.com as it won't exist in the registries config list and will not have
	// insecure=true, so we need to populate the registries config list with the blocked registries before moving on.
	for _, scope := range blockedScopes {
		_ = getRegistryEntry(scope)
	}

	// any of insecureScopes, blockedScopes, and mirrors, can be configured at a namespace/repo level,
	// and in V2RegistriesConf, only the most precise match is used; so, propagate the insecure/blocked
	// flags, and mirror configurations, to the child namespaces as well.
	for _, insecureScope := range insecureScopes {
		reg := getRegistryEntry(insecureScope)
		reg.Insecure = true
		for i := range config.Registries {
			reg := &config.Registries[i]
			if ScopeIsNestedInsideScope(registryScope(reg), insecureScope) {
				reg.Insecure = true
			}
			for j := range reg.Mirrors {
				mirror := &reg.Mirrors[j]
				if ScopeIsNestedInsideScope(mirror.Location, insecureScope) {
					mirror.Insecure = true
				}
			}
		}
	}
	for _, blockedScope := range blockedScopes {
		reg := getRegistryEntry(blockedScope)
		reg.Blocked = true
		for i := range config.Registries {
			reg := &config.Registries[i]
			if ScopeIsNestedInsideScope(registryScope(reg), blockedScope) {
				reg.Blocked = true
			}
		}
	}

	allMirrorSets := append(digestMirrorSets, tagMirrorSets...)
	for _, mirrorSet := range allMirrorSets {
		mirroredReg := getRegistryEntry(mirrorSet.source)
		mirroredScope := registryScope(mirroredReg)
		for i := range config.Registries {
			reg := &config.Registries[i]
			scope := registryScope(reg)
			// We have already iterated through all of mirrorSets.
			// So, if there is any mirror defined for a more specific sub-scope of mirrorSet.Source,
			// it must already exist with non-empty reg.Mirrors.
			if scope != mirroredScope && ScopeIsNestedInsideScope(scope, mirroredScope) && len(reg.Mirrors) == 0 {
				updated, err := mirrorsAdjustedForNestedScope(mirroredScope, scope, mirroredReg.Mirrors)
				if err != nil {
					return err
				}
				reg.Mirrors = updated
			}
		}
	}
	return nil
}

// IsValidRegistriesConfScope returns true if scope is a valid scope for the Prefix key in registries.conf
// This function can be used to validate the registries entries prior to calling EditRegistriesConfig
// in the MCO or builds code
func IsValidRegistriesConfScope(scope string) bool {
	if scope == "" {
		return false
	}
	// If scope does not contain the wildcard character, we will assume it is a regular registry entry, which is valid
	if !strings.Contains(scope, "*") {
		return true
	}
	// If it contains the wildcard character, check that it doesn't contain any invalid characters.
	// The only valid scope would be when it has the prefix "*."
	if strings.HasPrefix(scope, "*.") && !strings.ContainsAny(scope[2:], "/@:*") {
		return true
	}
	return false
}
