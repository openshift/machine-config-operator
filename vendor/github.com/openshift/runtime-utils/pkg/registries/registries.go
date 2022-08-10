package registries

import (
	"sort"
	"strings"

	"github.com/containers/image/v5/pkg/sysregistriesv2"
	apicfgv1 "github.com/openshift/api/config/v1"
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

// rdmContainsARealMirror returns true if mirrors contains at least one entry that is not source.
func rdmContainsARealMirror(source string, mirrors *[]apicfgv1.ImageMirror) bool {
	for _, mirror := range *mirrors {
		if string(mirror) != source {
			return true
		}
	}
	return false
}

// mirrorSet collects data from mirror setting CRDs (ImageDigestMirrorSet, ImageTagMirrorSet)
type mirrorSets struct {
	disjointSets      map[string]*[]*[]apicfgv1.ImageMirror // Key == Source
	mirrorBlockSource map[string]bool                       // key == Source
}

func newMirrorSets() *mirrorSets {
	return &mirrorSets{
		disjointSets:      map[string]*[]*[]apicfgv1.ImageMirror{},
		mirrorBlockSource: map[string]bool{},
	}
}

// sourceList collects the mirror sources and sort them in increasing order
func (sets *mirrorSets) sourceList() []string {
	sources := []string{}
	for key := range sets.disjointSets {
		sources = append(sources, key)
	}
	sort.Strings(sources)
	return sources
}

func (sets *mirrorSets) addMirrorSets(source string, mirrorSourcePolicy apicfgv1.MirrorSourcePolicy, mirrors *[]apicfgv1.ImageMirror) {
	if !rdmContainsARealMirror(source, mirrors) {
		return // No mirrors (or mirrors that only repeat the authoritative source) is not really a mirror set.
	}
	if mirrorSourcePolicy == apicfgv1.NeverContactSource {
		sets.mirrorBlockSource[source] = true
	}
	ds, ok := sets.disjointSets[source]
	if !ok {
		ds = &[]*[]apicfgv1.ImageMirror{}
		sets.disjointSets[source] = ds
	}
	*ds = append(*ds, mirrors)
}

// mergedMirrors generates deterministic order of mirrors for a given source
func (sets *mirrorSets) mergedMirrors(source string) (string, []apicfgv1.ImageMirror, error) {
	sortedRepos, err := topoSortRepos(source, sets.disjointSets[source])
	if err != nil {
		return "", nil, err
	}
	var mirrors []apicfgv1.ImageMirror
	for _, repo := range sortedRepos {
		mirrors = append(mirrors, apicfgv1.ImageMirror(repo))
	}
	return source, mirrors, nil
}

// mergedTagMirrorSets processes itmsRules and returns a set of ImageTagMirrors, one for each Source value,
// ordered consistently with the preference order of the individual entries (if possible)
// E.g. given mirror sets (B, C) and (A, B), it will combine them into a single (A, B, C) set.
func mergedTagMirrorSets(itmsRules []*apicfgv1.ImageTagMirrorSet) ([]apicfgv1.ImageTagMirrors, error) {
	tagMirrorSets := newMirrorSets()
	for _, itms := range itmsRules {
		for i := range itms.Spec.ImageTagMirrors {
			set := itms.Spec.ImageTagMirrors[i]
			tagMirrorSets.addMirrorSets(set.Source, set.MirrorSourcePolicy, &set.Mirrors)
		}
	}

	// Sort the sets of mirrors by Source to ensure deterministic output
	sources := tagMirrorSets.sourceList()
	// Convert the sets of mirrors
	res := []apicfgv1.ImageTagMirrors{}
	for _, source := range sources {
		source, mirrors, err := tagMirrorSets.mergedMirrors(source)
		if err != nil {
			return nil, err
		}
		imageTagMirror := apicfgv1.ImageTagMirrors{
			Source:  source,
			Mirrors: mirrors,
		}
		if tagMirrorSets.mirrorBlockSource[source] {
			imageTagMirror.MirrorSourcePolicy = apicfgv1.NeverContactSource
		}
		res = append(res, imageTagMirror)
	}
	return res, nil
}

// mergedDigestMirrorSets processes idmsRules and returns a set of ImageDigestMirrors, one for each Source value,
// ordered consistently with the preference order of the individual entries (if possible)
// E.g. given mirror sets (B, C) and (A, B), it will combine them into a single (A, B, C) set.
func mergedDigestMirrorSets(idmsRules []*apicfgv1.ImageDigestMirrorSet) ([]apicfgv1.ImageDigestMirrors, error) {

	digestMirrorSets := newMirrorSets()
	for _, idms := range idmsRules {
		for i := range idms.Spec.ImageDigestMirrors {
			set := idms.Spec.ImageDigestMirrors[i]
			digestMirrorSets.addMirrorSets(set.Source, set.MirrorSourcePolicy, &set.Mirrors)
		}
	}

	// Sort the sets of mirrors by Source to ensure deterministic output
	sources := digestMirrorSets.sourceList()
	// Convert the sets of mirrors
	res := []apicfgv1.ImageDigestMirrors{}
	for _, source := range sources {
		source, mirrors, err := digestMirrorSets.mergedMirrors(source)
		if err != nil {
			return nil, err
		}
		imageDigestMirror := apicfgv1.ImageDigestMirrors{
			Source:  source,
			Mirrors: mirrors,
		}
		if digestMirrorSets.mirrorBlockSource[source] {
			imageDigestMirror.MirrorSourcePolicy = apicfgv1.NeverContactSource
		}
		res = append(res, imageDigestMirror)

	}
	return res, nil
}

func topoSortRepos(source string, ds *[]*[]apicfgv1.ImageMirror) ([]string, error) {
	topoGraph := newTopoGraph()
	for _, set := range *ds {
		mirrors := *set
		for i := 0; i+1 < len(mirrors); i++ {
			topoGraph.AddEdge(string(mirrors[i]), string(mirrors[i+1]))
		}
		sourceInGraph := false
		for _, m := range mirrors {
			if string(m) == source {
				sourceInGraph = true
				break
			}
		}
		if !sourceInGraph {
			// The build of mirrorSets guarantees len(set.Mirrors) > 0.
			topoGraph.AddEdge(string(mirrors[len(mirrors)-1]), source)
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
	return sortedRepos, nil
}

func updateRegistry(mirrors []apicfgv1.ImageMirror, mirrorSourcePolicy apicfgv1.MirrorSourcePolicy, pullFromMirror string, reg *sysregistriesv2.Registry) {
	if mirrorSourcePolicy == apicfgv1.NeverContactSource {
		reg.Blocked = true
	}
	for _, mirror := range mirrors {
		reg.Mirrors = append(reg.Mirrors, sysregistriesv2.Endpoint{Location: string(mirror), PullFromMirror: pullFromMirror})
	}
}

// EditRegistriesConfig edits, IN PLACE, the /etc/containers/registries.conf configuration provided in config, to:
// - Mark scope entries in insecureScopes as insecure (TLS is not required, and TLS certificate verification is not required when TLS is used)
// - Mark scope entries in blockedScopes as blocked (any attempts to access them fail)
// - Implement ImageDigestMirrorSet rules in idmsRules.
// - Implement ImageTagMirrorSet rules in itmsRules.
// "scopes" can be any of whole registries, which means that the configuration applies to everything on that registry, including any possible separately-configured
// namespaces/repositories within that registry.
// or can be wildcard entries, which means that we accept wildcards in the form of *.example.registry.com for insecure and blocked registries only. We do not
// accept them for mirror configuration.
// A valid scope is in the form of registry/namespace...[/repo] (can also refer to sysregistriesv2.Registry.Prefix)
// NOTE: Validation of wildcard entries is done before EditRegistriesConfig is called in the MCO code.
func EditRegistriesConfig(config *sysregistriesv2.V2RegistriesConf, insecureScopes, blockedScopes []string,
	idmsRules []*apicfgv1.ImageDigestMirrorSet, itmsRules []*apicfgv1.ImageTagMirrorSet) error {

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
			reg := config.Registries[i].Location
			if config.Registries[i].Prefix != "" {
				reg = config.Registries[i].Prefix
			}
			if reg == scope {
				return &config.Registries[i]
			}
		}
		return addRegistryEntry(scope)
	}

	digestMirrorSets, err := mergedDigestMirrorSets(idmsRules)
	if err != nil {
		return err
	}
	for _, mirrorSet := range digestMirrorSets {
		reg := getRegistryEntry(mirrorSet.Source)
		updateRegistry(mirrorSet.Mirrors, mirrorSet.MirrorSourcePolicy, sysregistriesv2.MirrorByDigestOnly, reg)
	}

	tagMirrorSets, err := mergedTagMirrorSets(itmsRules)
	if err != nil {
		return err
	}
	for _, mirrorSet := range tagMirrorSets {
		reg := getRegistryEntry(mirrorSet.Source)
		updateRegistry(mirrorSet.Mirrors, mirrorSet.MirrorSourcePolicy, sysregistriesv2.MirrorByTagOnly, reg)
	}

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
	// flags to the child namespaces as well.
	for _, insecureScope := range insecureScopes {
		reg := getRegistryEntry(insecureScope)
		reg.Insecure = true
		for i := range config.Registries {
			reg := &config.Registries[i]
			scope := reg.Location
			// Set scope to Prefix if it exists
			if reg.Prefix != "" {
				scope = reg.Prefix
			}
			if ScopeIsNestedInsideScope(scope, insecureScope) {
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
			scope := reg.Location
			// Set scope to Prefix if it exists
			if reg.Prefix != "" {
				scope = reg.Prefix
			}
			if ScopeIsNestedInsideScope(scope, blockedScope) {
				reg.Blocked = true
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
