package mirror

import "testing"

func TestBatchTarget(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain mirror path", "http://h:3000/mirror", "http://h:3000/mirror/batch"},
		{"trailing slash", "http://h:3000/mirror/", "http://h:3000/mirror/batch"},
		{"already batched", "http://h:3000/mirror/batch", "http://h:3000/mirror/batch"},
		{"no path", "http://h:3000", "http://h:3000/batch"},
	}
	for _, tc := range cases {
		if got := batchTarget(tc.in); got != tc.want {
			t.Errorf("%s: batchTarget(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
