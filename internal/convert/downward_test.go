package convert

import "testing"

func TestResolveFieldRef(t *testing.T) {
	w := workload{name: "mongodb"}
	cases := []struct {
		path string
		want string
		ok   bool
	}{
		{"metadata.name", "mongodb", true},
		{"metadata.namespace", "default", true},
		{"metadata.uid", "mongodb-local", true},
		{"status.podIP", "mongodb", true},
		{"status.hostIP", "mongodb", true},
		{"spec.nodeName", "docker-host", true},
		{"spec.serviceAccountName", "default", true},
		{"metadata.labels['app']", "", false}, // not supported
		{"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got, ok := resolveFieldRef(tc.path, w)
			if ok != tc.ok || got != tc.want {
				t.Errorf("resolveFieldRef(%q) = (%q, %v), want (%q, %v)", tc.path, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestExpandRefs(t *testing.T) {
	env := map[string]string{
		"POD_NAME":      "mongodb",
		"POD_NAMESPACE": "default",
		"REGION":        "eu1",
	}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "single ref",
			in:   "$(POD_NAME)",
			want: "mongodb",
		},
		{
			name: "ref inside hostname (the mp-production case)",
			in:   "$(POD_NAME).headless.$(POD_NAMESPACE).svc.cluster.local",
			want: "mongodb.headless.default.svc.cluster.local",
		},
		{
			name: "multiple refs of same var",
			in:   "$(POD_NAME)-$(POD_NAME)",
			want: "mongodb-mongodb",
		},
		{
			name: "unknown var passes through literally so user can see what was meant",
			in:   "$(UNKNOWN).example.com",
			want: "$(UNKNOWN).example.com",
		},
		{
			name: "literal dollar via $$",
			in:   "price=$$5 for $(REGION)",
			want: "price=$5 for eu1",
		},
		{
			name: "no refs",
			in:   "plain string with no substitution",
			want: "plain string with no substitution",
		},
		{
			name: "bare dollar without paren",
			in:   "cost is $5 today",
			want: "cost is $5 today",
		},
		{
			name: "unterminated paren passes through",
			in:   "$(POD_NAME but never closed",
			want: "$(POD_NAME but never closed",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expandRefs(tc.in, env)
			if got != tc.want {
				t.Errorf("expandRefs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
