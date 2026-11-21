package bootimage

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	// awsCredentialsProfileKey is the Secret data key holding the shared credentials INI document
	// written by the Cloud Credential Operator (mint and passthrough modes) and by ccoctl (STS).
	awsCredentialsProfileKey = "credentials"
	// awsDefaultProfile is the only INI profile written by the Cloud Credential Operator and ccoctl.
	awsDefaultProfile = "default"

	awsRoleARNKey              = "role_arn"
	awsWebIdentityTokenFileKey = "web_identity_token_file"
	awsAccessKeyIDKey          = "aws_access_key_id"
	awsSecretAccessKeyKey      = "aws_secret_access_key"
)

// awsCredentialKeys lists the keys consumed from either the top-level Secret data or the
// [default] profile of the credentials INI document.
var awsCredentialKeys = []string{awsRoleARNKey, awsWebIdentityTokenFileKey, awsAccessKeyIDKey, awsSecretAccessKeyKey}

// awsCredentials holds the credential settings resolved from an AWS credentials Secret.
// Exactly one of the STS web identity pair (roleARN, webIdentityTokenFile) or the static
// key pair (accessKeyID, secretAccessKey) is set.
type awsCredentials struct {
	roleARN              string
	webIdentityTokenFile string
	accessKeyID          string
	secretAccessKey      string
}

// usesWebIdentity reports whether the credentials use STS AssumeRoleWithWebIdentity.
func (c awsCredentials) usesWebIdentity() bool {
	return c.roleARN != ""
}

// awsCredentialSource is one place in the Secret from which credential keys can be read.
type awsCredentialSource struct {
	name   string
	values map[string]string
}

// resolveAWSCredentials extracts AWS credentials from a Secret in the formats produced by the
// Cloud Credential Operator and ccoctl, and from manually created Secrets:
//
//   - the "credentials" key holding an INI document whose [default] profile sets role_arn and
//     web_identity_token_file (ccoctl STS) or aws_access_key_id and aws_secret_access_key
//     (mint and passthrough modes);
//   - top-level data keys with the same names.
//
// STS web identity credentials take precedence over static keys, and the credentials profile is
// consulted before top-level keys. Partially configured credentials are rejected rather than
// silently skipped. Returned errors name the Secret and the keys involved but never include
// credential values.
func resolveAWSCredentials(secret *corev1.Secret) (awsCredentials, error) {
	ref := secret.Namespace + "/" + secret.Name

	var sources []awsCredentialSource
	var profileErr error
	if raw := secret.Data[awsCredentialsProfileKey]; len(bytes.TrimSpace(raw)) > 0 {
		profile, err := parseAWSCredentialsProfile(raw)
		if err != nil {
			profileErr = err
		} else {
			sources = append(sources, awsCredentialSource{
				name:   fmt.Sprintf("the [%s] profile of the %q key", awsDefaultProfile, awsCredentialsProfileKey),
				values: profile,
			})
		}
	}

	topLevel := map[string]string{}
	for _, key := range awsCredentialKeys {
		if value := strings.TrimSpace(string(secret.Data[key])); value != "" {
			topLevel[key] = value
		}
	}
	sources = append(sources, awsCredentialSource{name: "the top-level data keys", values: topLevel})

	roleARN, tokenFile, partialSTS := findAWSCredentialPair(sources, awsRoleARNKey, awsWebIdentityTokenFileKey)
	accessKeyID, secretAccessKey, partialStatic := findAWSCredentialPair(sources, awsAccessKeyIDKey, awsSecretAccessKeyKey)

	var creds awsCredentials
	switch {
	case roleARN != "":
		if !filepath.IsAbs(tokenFile) {
			// Do not echo the value: a relative "path" may be misplaced token text.
			return awsCredentials{}, fmt.Errorf("secret %s: %s must be an absolute path", ref, awsWebIdentityTokenFileKey)
		}
		creds = awsCredentials{roleARN: roleARN, webIdentityTokenFile: tokenFile}
	case partialSTS != "":
		return awsCredentials{}, fmt.Errorf("secret %s: incomplete STS web identity credentials in %s: both %s and %s are required",
			ref, partialSTS, awsRoleARNKey, awsWebIdentityTokenFileKey)
	case accessKeyID != "":
		creds = awsCredentials{accessKeyID: accessKeyID, secretAccessKey: secretAccessKey}
	case partialStatic != "":
		return awsCredentials{}, fmt.Errorf("secret %s: incomplete static credentials in %s: both %s and %s are required",
			ref, partialStatic, awsAccessKeyIDKey, awsSecretAccessKeyKey)
	default:
		err := fmt.Errorf("secret %s contains no usable AWS credentials: expected %s and %s (STS web identity) or %s and %s (static keys), "+
			"either in the [%s] profile of the %q key or as top-level data keys",
			ref, awsRoleARNKey, awsWebIdentityTokenFileKey, awsAccessKeyIDKey, awsSecretAccessKeyKey, awsDefaultProfile, awsCredentialsProfileKey)
		if profileErr != nil {
			return awsCredentials{}, fmt.Errorf("%w; the %q key could not be parsed: %w", err, awsCredentialsProfileKey, profileErr)
		}
		return awsCredentials{}, err
	}

	if profileErr != nil {
		klog.Warningf("Secret %s: ignoring the %q key, which could not be parsed (%v); using the top-level data keys instead",
			ref, awsCredentialsProfileKey, profileErr)
	}
	return creds, nil
}

// findAWSCredentialPair returns the values of the first source that sets both keys. When no
// source sets both, it returns the name of the first source that sets only one of them so that
// the caller can reject the partial configuration.
func findAWSCredentialPair(sources []awsCredentialSource, firstKey, secondKey string) (first, second, partialSource string) {
	for _, src := range sources {
		a, b := src.values[firstKey], src.values[secondKey]
		if a != "" && b != "" {
			return a, b, ""
		}
		if (a != "" || b != "") && partialSource == "" {
			partialSource = src.name
		}
	}
	return "", "", partialSource
}

// parseAWSCredentialsProfile returns the key/value pairs of the [default] profile in the INI
// document stored in the "credentials" key. It intentionally supports only the subset of the
// shared credentials format that the Cloud Credential Operator and ccoctl write: "[section]"
// headers, "key = value" lines, blank lines and full-line comments starting with "#" or ";".
// Inline comments, quoting and continuation lines are not supported; a credential value that
// contains whitespace is rejected rather than misread. Errors identify line numbers but never
// line contents, because the document contains secret material.
func parseAWSCredentialsProfile(data []byte) (map[string]string, error) {
	profile := map[string]string{}
	section := ""
	sawDefault := false

	for i, line := range strings.Split(string(data), "\n") {
		lineNum := i + 1
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: malformed section header", lineNum)
			}
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section == "" {
				return nil, fmt.Errorf("line %d: empty section name", lineNum)
			}
			if section == awsDefaultProfile {
				if sawDefault {
					return nil, fmt.Errorf("line %d: duplicate [%s] section", lineNum, awsDefaultProfile)
				}
				sawDefault = true
			}
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil, fmt.Errorf("line %d: expected a \"key = value\" pair", lineNum)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return nil, fmt.Errorf("line %d: missing key name", lineNum)
		}
		if section == "" {
			return nil, fmt.Errorf("line %d: key/value pair appears before any section header", lineNum)
		}
		if section != awsDefaultProfile {
			continue
		}
		if _, duplicate := profile[key]; duplicate {
			return nil, fmt.Errorf("line %d: duplicate key in the [%s] section", lineNum, awsDefaultProfile)
		}
		if slices.Contains(awsCredentialKeys, key) && strings.ContainsAny(value, " \t") {
			return nil, fmt.Errorf("line %d: the value of %s contains whitespace; inline comments and quoting are not supported", lineNum, key)
		}
		profile[key] = value
	}

	if !sawDefault {
		return nil, fmt.Errorf("no [%s] section found", awsDefaultProfile)
	}
	return profile, nil
}

// checkWebIdentityTokenFile verifies, when the EC2 client is built, that the token file
// referenced by the credentials is a readable regular file. Symbolic links are followed, so the
// projected service account token layout (token -> ..data/token) is accepted.
//
// The target is inspected with stat before it is opened, so FIFOs, devices and other
// non-regular files are rejected without being opened: opening a FIFO for reading blocks until
// a writer appears, and the SDK reads the whole file on each credential refresh, which never
// finishes for devices such as /dev/zero. The file is then opened non-blocking and inspected
// again through the open descriptor in case the target changed in between. The token is never
// read or logged.
//
// The guarantee only covers the moment of the check. The AWS SDK re-reads the path on every
// credential refresh without these checks, so a target replaced on the container filesystem
// after the client is built is not detected here. A change to the Secret rebuilds the client
// and repeats the check.
func checkWebIdentityTokenFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return unreadableTokenFileError(path, err)
	}
	if err := requireRegularTokenFile(path, info); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return unreadableTokenFileError(path, err)
	}
	defer f.Close()

	opened, err := f.Stat()
	if err != nil {
		return fmt.Errorf("web identity token file %q cannot be inspected: %w", path, err)
	}
	return requireRegularTokenFile(path, opened)
}

func unreadableTokenFileError(path string, err error) error {
	return fmt.Errorf("web identity token file %q is not readable; the machine-config-controller pod must mount the projected service account token: %w", path, err)
}

func requireRegularTokenFile(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("web identity token file %q is not a regular file (file type %q)", path, info.Mode().Type().String())
	}
	return nil
}
