package handler

import "testing"

func TestNormalizeDiscoverPreferenceSectionsOrdersAndValidates(t *testing.T) {
	sections := []discoverSectionDef{
		{Key: "first"},
		{Key: "second"},
		{Key: "adult", Group: "adult"},
	}
	got, err := normalizeDiscoverPreferenceSections([]string{"second", "first", "second"}, sections, true, true)
	if err != nil || len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("got = %#v err=%v", got, err)
	}
	if _, err := normalizeDiscoverPreferenceSections([]string{"missing"}, sections, true, true); err == nil {
		t.Fatal("unknown section should be rejected")
	}
	got, err = normalizeDiscoverPreferenceSections([]string{"adult", "first"}, sections, false, false)
	if err != nil || len(got) != 1 || got[0] != "first" {
		t.Fatalf("adult filtered result = %#v err=%v", got, err)
	}
}
