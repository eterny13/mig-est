// Package mysqlver parses MySQL server versions and answers feature-gate
// questions used by the classifier.
package mysqlver

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed MySQL 8.0/8.4 version.
type Version struct {
	Raw        string
	Major      int
	Minor      int
	Patch      int
	PatchKnown bool
}

// Parse parses the leading major.minor.patch of a MySQL version string.
// Suffixes such as "-log" or "-0ubuntu0.22.04.1" are ignored.
func Parse(raw string) (Version, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Version{}, fmt.Errorf("empty MySQL version")
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "mariadb") {
		return Version{}, fmt.Errorf("MariaDB is not supported (found %q)", raw)
	}
	base := raw
	if i := strings.IndexAny(base, "-+ "); i >= 0 {
		base = base[:i]
	}
	parts := strings.Split(base, ".")
	if len(parts) < 2 {
		return Version{}, fmt.Errorf("cannot parse MySQL version %q (expected major.minor[.patch])", raw)
	}
	v := Version{Raw: raw}
	var err error
	if v.Major, err = strconv.Atoi(parts[0]); err != nil {
		return Version{}, fmt.Errorf("cannot parse MySQL version %q", raw)
	}
	if v.Minor, err = strconv.Atoi(parts[1]); err != nil {
		return Version{}, fmt.Errorf("cannot parse MySQL version %q", raw)
	}
	if len(parts) >= 3 {
		patch := parts[2]
		if i := strings.IndexFunc(patch, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
			patch = patch[:i]
		}
		if patch == "" {
			return Version{}, fmt.Errorf("cannot parse MySQL version %q", raw)
		}
		if v.Patch, err = strconv.Atoi(patch); err != nil {
			return Version{}, fmt.Errorf("cannot parse MySQL version %q", raw)
		}
		v.PatchKnown = true
	}
	return v, nil
}

// IsSupported reports whether this is a MySQL series supported by v0.1.
func (v Version) IsSupported() bool {
	return v.Major == 8 && (v.Minor == 0 || v.Minor == 4)
}

// AtLeast reports whether v >= major.minor.patch. If the patch level is
// unknown, an equal major/minor series is assumed to be current.
func (v Version) AtLeast(major, minor, patch int) bool {
	if v.Major != major {
		return v.Major > major
	}
	if v.Minor != minor {
		return v.Minor > minor
	}
	if !v.PatchKnown {
		return true
	}
	return v.Patch >= patch
}

// String returns a normalized version string.
func (v Version) String() string {
	if v.PatchKnown {
		return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	}
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// Display returns the original version string when available.
func (v Version) Display() string {
	if v.Raw != "" {
		return v.Raw
	}
	return v.String()
}
