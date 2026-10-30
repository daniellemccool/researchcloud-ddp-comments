package access

import "strings"

// IsRole reports whether the roles header holds role as a WHOLE
// ';'-separated segment: "src_co_students_x" is not "src_co_students".
// Surrounding spaces are trimmed per segment; an empty header is false;
// leading, trailing and double ';' are tolerated.
func IsRole(header, role string) bool {
	if role == "" {
		return false
	}
	for _, seg := range strings.Split(header, ";") {
		if strings.TrimSpace(seg) == role {
			return true
		}
	}
	return false
}

// splitRoles returns the non-empty trimmed ';'-separated segments of the
// roles header, in order. The result is never nil so it marshals as a JSON
// array.
func splitRoles(header string) []string {
	roles := []string{}
	for _, seg := range strings.Split(header, ";") {
		if seg = strings.TrimSpace(seg); seg != "" {
			roles = append(roles, seg)
		}
	}
	return roles
}
