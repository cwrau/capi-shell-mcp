package capi

import "testing"

func TestFormatTargetJoinsFieldsWithSlash(t *testing.T) {
	got := FormatTarget(CAPICluster{KubeconfigName: "prod", Context: "prod-admin", Namespace: "default", Name: "my-cluster"})
	if want := "prod/prod-admin/default/my-cluster"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseTargetRecoversAllFourFields(t *testing.T) {
	kubeconfigName, context, namespace, name, err := ParseTarget("prod/prod-admin/default/my-cluster")
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	if kubeconfigName != "prod" || context != "prod-admin" || namespace != "default" || name != "my-cluster" {
		t.Errorf("got (%q, %q, %q, %q)", kubeconfigName, context, namespace, name)
	}
}

func TestParseTargetErrorsOnMalformedInput(t *testing.T) {
	for _, bad := range []string{"", "prod", "prod/ctx", "prod/ctx/ns"} {
		if _, _, _, _, err := ParseTarget(bad); err == nil {
			t.Errorf("ParseTarget(%q): expected error, got nil", bad)
		}
	}
}

func TestFormatTargetThenParseTargetRoundTrips(t *testing.T) {
	c := CAPICluster{KubeconfigName: "prod", Context: "prod-admin", Namespace: "default", Name: "my-cluster"}
	kubeconfigName, context, namespace, name, err := ParseTarget(FormatTarget(c))
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	if kubeconfigName != c.KubeconfigName || context != c.Context || namespace != c.Namespace || name != c.Name {
		t.Errorf("round trip got (%q, %q, %q, %q), want %+v", kubeconfigName, context, namespace, name, c)
	}
}
