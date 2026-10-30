package access

import "testing"

func TestIsRoleWholeSegment(t *testing.T) {
	cases := []struct {
		header string
		role   string
		want   bool
	}{
		{"src_co_students_x", "src_co_students", false},
		{"x;src_co_students;y", "src_co_students", true},
		{";src_co_students;", "src_co_students", true},
		{" src_co_students ", "src_co_students", true},
		{"", "src_co_students", false},
		{"src_co_student", "src_co_students", false},
		{"src_co_students;src_co_admin", "src_co_admin", true},
		{"src_co_students", "src_co_admin", false},
		{";;src_co_admin;;", "src_co_admin", true},
		{" src_co_admin ; src_co_students ", "src_co_admin", true},
	}
	for _, c := range cases {
		if got := IsRole(c.header, c.role); got != c.want {
			t.Errorf("IsRole(%q, %q) = %v, want %v", c.header, c.role, got, c.want)
		}
	}
}
