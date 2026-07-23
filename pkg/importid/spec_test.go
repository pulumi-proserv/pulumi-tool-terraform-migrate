package importid

import "testing"

func TestCompose_PureJoins(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		pulumiType string
		provider   string
		attrs      map[Role]string
		want       string
	}{
		{"lambda permission", "aws:lambda/permission:Permission", "classic",
			map[Role]string{RoleFunction: "ffs-dev-api", RoleStatement: "AllowS3"}, "ffs-dev-api/AllowS3"},
		{"apigw resource classic", "aws:apigateway/resource:Resource", "classic",
			map[Role]string{RoleRestApi: "abc", RoleID: "res"}, "abc/res"},
		{"apigw deployment native reversed", "aws:apigateway/deployment:Deployment", "native",
			map[Role]string{RoleRestApi: "abc", RoleID: "dep"}, "dep|abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			get := func(r Role) string { return tc.attrs[r] }
			got, handled, err := Compose(tc.pulumiType, tc.provider, get)
			if err != nil || !handled || got != tc.want {
				t.Fatalf("Compose = %q handled=%v err=%v; want %q", got, handled, err, tc.want)
			}
		})
	}
}

func TestCompose_Unhandled(t *testing.T) {
	t.Parallel()
	_, handled, err := Compose("aws:iam/policy:Policy", "classic", func(Role) string { return "" })
	if err != nil || handled {
		t.Fatalf("expected unhandled, no error; got handled=%v err=%v", handled, err)
	}
}
