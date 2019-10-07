package v1

// TLSSecurityProfile defines the schema for a TLS security profile. This object
// is used by operators to apply TLS security settings to operands.
// +union
type TLSSecurityProfile struct {
	// type is one of Old, Intermediate, Modern or Custom. Custom provides
	// the ability to specify individual TLS security profile parameters.
	// Old, Intermediate and Modern are TLS security profiles based on:
	//
	// https://wiki.mozilla.org/Security/Server_Side_TLS#Recommended_configurations
	//
	// The profiles are intent based, so they may change over time as new ciphers are developed and existing ciphers
	// are found to be insecure.  Depending on precisely which ciphers are available to a process, the list may be
	// reduced.
	//
	// +unionDiscriminator
	// +optional
	Type TLSProfileType `json:"type"`
	// old is a TLS security profile based on:
	//
	// https://wiki.mozilla.org/Security/Server_Side_TLS#Old_backward_compatibility
	//
	// and looks like this (yaml):
	//
	//   ciphers:
	//     - TLS_AES_128_GCM_SHA256
	//     - TLS_AES_256_GCM_SHA384
	//     - TLS_CHACHA20_POLY1305_SHA256
	//     - ECDHE-ECDSA-AES128-GCM-SHA256
	//     - ECDHE-RSA-AES128-GCM-SHA256
	//     - ECDHE-ECDSA-AES256-GCM-SHA384
	//     - ECDHE-RSA-AES256-GCM-SHA384
	//     - ECDHE-ECDSA-CHACHA20-POLY1305
	//     - ECDHE-RSA-CHACHA20-POLY1305
	//     - DHE-RSA-AES128-GCM-SHA256
	//     - DHE-RSA-AES256-GCM-SHA384
	//     - DHE-RSA-CHACHA20-POLY1305
	//     - ECDHE-ECDSA-AES128-SHA256
	//     - ECDHE-RSA-AES128-SHA256
	//     - ECDHE-ECDSA-AES128-SHA
	//     - ECDHE-RSA-AES128-SHA
	//     - ECDHE-ECDSA-AES256-SHA384
	//     - ECDHE-RSA-AES256-SHA384
	//     - ECDHE-ECDSA-AES256-SHA
	//     - ECDHE-RSA-AES256-SHA
	//     - DHE-RSA-AES128-SHA256
	//     - DHE-RSA-AES256-SHA256
	//     - AES128-GCM-SHA256
	//     - AES256-GCM-SHA384
	//     - AES128-SHA256
	//     - AES256-SHA256
	//     - AES128-SHA
	//     - AES256-SHA
	//     - DES-CBC3-SHA
	//   tlsVersion:
	//     minimumVersion: TLSv1.0
	//     maximumVersion: TLSv1.3
	//   dhParamSize: 1024
	//
	// +optional
	// +nullable
	Old *OldTLSProfile `json:"old,omitempty"`
	// intermediate is a TLS security profile based on:
	//
	// https://wiki.mozilla.org/Security/Server_Side_TLS#Intermediate_compatibility_.28recommended.29
	//
	// and looks like this (yaml):
	//
	//   ciphers:
	//     - TLS_AES_128_GCM_SHA256
	//     - TLS_AES_256_GCM_SHA384
	//     - TLS_CHACHA20_POLY1305_SHA256
	//     - ECDHE-ECDSA-AES128-GCM-SHA256
	//     - ECDHE-RSA-AES128-GCM-SHA256
	//     - ECDHE-ECDSA-AES256-GCM-SHA384
	//     - ECDHE-RSA-AES256-GCM-SHA384
	//     - ECDHE-ECDSA-CHACHA20-POLY1305
	//     - ECDHE-RSA-CHACHA20-POLY1305
	//     - DHE-RSA-AES128-GCM-SHA256
	//     - DHE-RSA-AES256-GCM-SHA384
	//   tlsVersion:
	//     minimumVersion: TLSv1.2
	//     maximumVersion: TLSv1.3
	//   dhParamSize: 2048
	//
	// +optional
	// +nullable
	Intermediate *IntermediateTLSProfile `json:"intermediate,omitempty"`
	// modern is a TLS security profile based on:
	//
	// https://wiki.mozilla.org/Security/Server_Side_TLS#Modern_compatibility
	//
	// and looks like this (yaml):
	//
	//   ciphers:
	//     - TLS_AES_128_GCM_SHA256
	//     - TLS_AES_256_GCM_SHA384
	//     - TLS_CHACHA20_POLY1305_SHA256
	//   tlsVersion:
	//     minimumVersion: TLSv1.3
	//     maximumVersion: TLSv1.3
	//   dhParamSize: 2048
	//
	// +optional
	// +nullable
	Modern *ModernTLSProfile `json:"modern,omitempty"`
	// custom is a user-defined TLS security profile. Be extremely careful using a custom
	// profile as invalid configurations can be catastrophic. An example custom profile
	// looks like this:
	//
	//   ciphers:
	//     - ECDHE-ECDSA-CHACHA20-POLY1305
	//     - ECDHE-RSA-CHACHA20-POLY1305
	//     - ECDHE-RSA-AES128-GCM-SHA256
	//     - ECDHE-ECDSA-AES128-GCM-SHA256
	//   tlsVersion:
	//     minimumVersion: TLSv1.1
	//     maximumVersion: TLSv1.2
	//   dhParamSize: 1024
	//
	// +optional
	// +nullable
	Custom *CustomTLSProfile `json:"custom,omitempty"`
}

// OldTLSProfile is a TLS security profile based on:
// https://wiki.mozilla.org/Security/Server_Side_TLS#Old_backward_compatibility
type OldTLSProfile struct{}

// IntermediateTLSProfile is a TLS security profile based on:
// https://wiki.mozilla.org/Security/Server_Side_TLS#Intermediate_compatibility_.28default.29
type IntermediateTLSProfile struct{}

// ModernTLSProfile is a TLS security profile based on:
// https://wiki.mozilla.org/Security/Server_Side_TLS#Modern_compatibility
type ModernTLSProfile struct{}

// CustomTLSProfile is a user-defined TLS security profile. Be extremely careful
// using a custom TLS profile as invalid configurations can be catastrophic.
type CustomTLSProfile struct {
	TLSProfileSpec `json:",inline"`
}

// TLSProfileType defines a TLS security profile type.
type TLSProfileType string

const (
	// Old is a TLS security profile based on:
	// https://wiki.mozilla.org/Security/Server_Side_TLS#Old_backward_compatibility
	TLSProfileOldType TLSProfileType = "Old"
	// Intermediate is a TLS security profile based on:
	// https://wiki.mozilla.org/Security/Server_Side_TLS#Intermediate_compatibility_.28default.29
	TLSProfileIntermediateType TLSProfileType = "Intermediate"
	// Modern is a TLS security profile based on:
	// https://wiki.mozilla.org/Security/Server_Side_TLS#Modern_compatibility
	TLSProfileModernType TLSProfileType = "Modern"
	// Custom is a TLS security profile that allows for user-defined parameters.
	TLSProfileCustomType TLSProfileType = "Custom"
)

// TLSProfileSpec is the desired behavior of a TLSSecurityProfile.
type TLSProfileSpec struct {
	// ciphers is used to specify the cipher algorithms that are negotiated
	// during the TLS handshake.  Operators may remove entries their operands
	// do not support.  For example, to use 3DES  (yaml):
	//
	//   ciphers:
	//     - 3DES
	//
	Ciphers []string `json:"ciphers"`
	// tlsVersion is used to specify one or more versions of the TLS protocol
	// that is negotiated during the TLS handshake. For example, to use TLS
	// versions 1.1, 1.2 and 1.3 (yaml):
	//
	//   tlsVersion:
	//     minimumVersion: TLSv1.1
	//     maximumVersion: TLSv1.3
	//
	TLSVersion TLSVersion `json:"tlsVersion"`
	// dhParamSize sets the maximum size of the Diffie-Hellman parameters used for generating
	// the ephemeral/temporary Diffie-Hellman key in case of DHE key exchange. The final size
	// will try to match the size of the server's RSA (or DSA) key (e.g, a 2048 bits temporary
	// DH key for a 2048 bits RSA key), but will not exceed this maximum value.
	//
	// Available DH Parameter sizes are:
	//
	//   "2048": A Diffie-Hellman parameter of 2048 bits.
	//   "1024": A Diffie-Hellman parameter of 1024 bits.
	//
	// For example, to use a Diffie-Hellman parameter of 2048 bits (yaml):
	//
	//   dhParamSize: 2048
	//
	DHParamSize DHParamSize `json:"dhParamSize"`
}

// TLSVersion defines one or more versions of the TLS protocol that are negotiated
// during the TLS handshake.
type TLSVersion struct {
	// minimumVersion enforces use of the specified TLSProtocolVersion or newer
	// that are negotiated during the TLS handshake. minimumVersion must be lower
	// than or equal to maximumVersion.
	//
	// If unset and maximumVersion is set, minimumVersion will be set
	// to maximumVersion. If minimumVersion and maximumVersion are unset,
	// the minimum version is determined by the TLS security profile type.
	//
	//   TLSProfileType Modern:       VersionTLS13
	//   TLSProfileType Intermediate: VersionTLS12
	//   TLSProfileType Old:          VersionTLS10
	//
	// Supported minimum versions are:
	//
	//   "TLSv1.3": Version 1.3 of the TLS security protocol.
	//   "TLSv1.2": Version 1.2 of the TLS security protocol.
	//   "TLSv1.1": Version 1.1 of the TLS security protocol.
	//   "TLSv1.0": Version 1.0 of the TLS security protocol.
	//
	MinimumVersion TLSProtocolVersion `json:"minimumVersion"`
	// maximumVersion enforces use of the specified TLSProtocolVersion or older
	// that are negotiated during the TLS handshake. maximumVersion must be higher
	// than or equal to minimumVersion.
	//
	// If unset and minimumVersion is set, maximumVersion will be set
	// to minimumVersion. If minimumVersion and maximumVersion are unset,
	// the maximum version is determined by the TLS security profile type.
	//
	//   TLSProfileType Modern:       VersionTLS13
	//   TLSProfileType Intermediate: VersionTLS13
	//   TLSProfileType Old:          VersionTLS13
	//
	// Supported maximum versions are the same as minimum versions.
	//
	MaximumVersion TLSProtocolVersion `json:"maximumVersion"`
}

// TLSProtocolVersion is a way to specify the protocol version used for TLS connections.
// Protocol versions are based on the following most common TLS configurations:
//
//   https://ssl-config.mozilla.org/
//
// Note that SSLv3.0 is not a supported protocol version due to well known
// vulnerabilities such as POODLE: https://en.wikipedia.org/wiki/POODLE
type TLSProtocolVersion string

const (
	// TLSv1.0 is version 1.0 of the TLS security protocol.
	VersionTLS10 TLSProtocolVersion = "TLSv1.0"
	// TLSv1.1 is version 1.1 of the TLS security protocol.
	VersionTLS11 TLSProtocolVersion = "TLSv1.1"
	// TLSv1.2 is version 1.2 of the TLS security protocol.
	VersionTLS12 TLSProtocolVersion = "TLSv1.2"
	// TLSv1.3 is version 1.3 of the TLS security protocol.
	VersionTLS13 TLSProtocolVersion = "TLSv1.3"
)

// DHParamSize sets the maximum size of the Diffie-Hellman parameters used for
// generating the ephemeral/temporary Diffie-Hellman key.
type DHParamSize string

const (
	// 1024 is a Diffie-Hellman parameter of 1024 bits.
	DHParamSize1024 DHParamSize = "1024"
	// 2048 is a Diffie-Hellman parameter of 2048 bits.
	DHParamSize2048 DHParamSize = "2048"
)

// TLSProfiles Contains a map of TLSProfileType names to TLSProfileSpec.
//
// NOTE: The caller needs to make sure to check that these constants are valid for their binary. Not all
// entries map to values for all binaries.  In the case of ties, the kube-apiserver wins.  Do not fail,
// just be sure to whitelist only and everything will be ok.
var TLSProfiles = map[TLSProfileType]*TLSProfileSpec{
	TLSProfileOldType: {
		Ciphers: []string{
			"TLS_AES_128_GCM_SHA256",
			"TLS_AES_256_GCM_SHA384",
			"TLS_CHACHA20_POLY1305_SHA256",
			"ECDHE-ECDSA-AES128-GCM-SHA256",
			"ECDHE-RSA-AES128-GCM-SHA256",
			"ECDHE-ECDSA-AES256-GCM-SHA384",
			"ECDHE-RSA-AES256-GCM-SHA384",
			"ECDHE-ECDSA-CHACHA20-POLY1305",
			"ECDHE-RSA-CHACHA20-POLY1305",
			"DHE-RSA-AES128-GCM-SHA256",
			"DHE-RSA-AES256-GCM-SHA384",
			"DHE-RSA-CHACHA20-POLY1305",
			"ECDHE-ECDSA-AES128-SHA256",
			"ECDHE-RSA-AES128-SHA256",
			"ECDHE-ECDSA-AES128-SHA",
			"ECDHE-RSA-AES128-SHA",
			"ECDHE-ECDSA-AES256-SHA384",
			"ECDHE-RSA-AES256-SHA384",
			"ECDHE-ECDSA-AES256-SHA",
			"ECDHE-RSA-AES256-SHA",
			"DHE-RSA-AES128-SHA256",
			"DHE-RSA-AES256-SHA256",
			"AES128-GCM-SHA256",
			"AES256-GCM-SHA384",
			"AES128-SHA256",
			"AES256-SHA256",
			"AES128-SHA",
			"AES256-SHA",
			"DES-CBC3-SHA",
		},
		TLSVersion: TLSVersion{
			MinimumVersion: VersionTLS10,
			MaximumVersion: VersionTLS13,
		},
		DHParamSize: DHParamSize1024,
	},
	TLSProfileIntermediateType: {
		Ciphers: []string{
			"TLS_AES_128_GCM_SHA256",
			"TLS_AES_256_GCM_SHA384",
			"TLS_CHACHA20_POLY1305_SHA256",
			"ECDHE-ECDSA-AES128-GCM-SHA256",
			"ECDHE-RSA-AES128-GCM-SHA256",
			"ECDHE-ECDSA-AES256-GCM-SHA384",
			"ECDHE-RSA-AES256-GCM-SHA384",
			"ECDHE-ECDSA-CHACHA20-POLY1305",
			"ECDHE-RSA-CHACHA20-POLY1305",
			"DHE-RSA-AES128-GCM-SHA256",
			"DHE-RSA-AES256-GCM-SHA384",
		},
		TLSVersion: TLSVersion{
			MinimumVersion: VersionTLS12,
			MaximumVersion: VersionTLS13,
		},
		DHParamSize: DHParamSize2048,
	},
	TLSProfileModernType: {
		Ciphers: []string{
			"TLS_AES_128_GCM_SHA256",
			"TLS_AES_256_GCM_SHA384",
			"TLS_CHACHA20_POLY1305_SHA256",
		},
		TLSVersion: TLSVersion{
			MinimumVersion: VersionTLS13,
			MaximumVersion: VersionTLS13,
		},
	},
}
