package hosted

import "testing"

func TestJobsRequireRaceOnlyForCompleteEvaluator(t *testing.T) {
	for _, test := range []struct {
		name  string
		lanes []string
		want  bool
	}{
		{name: "none", lanes: []string{"contracts"}},
		{name: "compat", lanes: []string{"contracts", "eval-compat"}},
		{name: "full", lanes: []string{"contracts", "eval-full", "platform", "security"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			jobs := (Plan{Lanes: test.lanes}).Jobs()
			if jobs["agent-eval-race"] != test.want {
				t.Fatalf("race selected=%v want=%v", jobs["agent-eval-race"], test.want)
			}
			if len(jobs) != 11 {
				t.Fatalf("aggregate jobs=%d, want 11", len(jobs))
			}
		})
	}
}
