package workitem

import (
	"regexp"
	"testing"
)

func TestGenerateID_PlanFormat(t *testing.T) {
	id := GenerateID("plan", "Webhook Support")
	matched, _ := regexp.MatchString(`^plan-[0-9a-f]{8}$`, id)
	if !matched {
		t.Errorf("GenerateID(plan, ...) = %q, want plan-{hex8}", id)
	}
}

func TestGenerateID_FeatureFormat(t *testing.T) {
	id := GenerateID("feature", "Add login")
	matched, _ := regexp.MatchString(`^feat-[0-9a-f]{8}$`, id)
	if !matched {
		t.Errorf("GenerateID(feature, ...) = %q, want feat-{hex8}", id)
	}
}

func TestGenerateID_Uniqueness(t *testing.T) {
	a := GenerateID("plan", "Same Title")
	b := GenerateID("plan", "Same Title")
	if a == b {
		t.Errorf("GenerateID produced identical IDs: %s", a)
	}
}

func TestGenerateID_AllPrefixes(t *testing.T) {
	cases := []struct {
		nodeType   string
		wantPrefix string
	}{
		{"feature", "feat"},
		{"bug", "bug"},
		{"track", "trk"},
		{"spike", "spk"},
		{"plan", "plan"},
		{"spec", "spec"},
		{"session", "sess"},
	}
	re := regexp.MustCompile(`^([a-z]+)-[0-9a-f]{8}$`)
	for _, tc := range cases {
		id := GenerateID(tc.nodeType, "test")
		m := re.FindStringSubmatch(id)
		if len(m) < 2 {
			t.Errorf("GenerateID(%q, ...) = %q, does not match prefix-hex8 format", tc.nodeType, id)
			continue
		}
		if m[1] != tc.wantPrefix {
			t.Errorf("GenerateID(%q, ...) prefix = %q, want %q", tc.nodeType, m[1], tc.wantPrefix)
		}
	}
}
