package agenteval

import "testing"

func TestRuntimeSkillActivationVocabulary(t *testing.T) {
	for _, activation := range []string{"", SkillActivationImplicit, SkillActivationExplicit, SkillActivationDeveloper, SkillActivationCombined} {
		runtime := Runtime{Provider: "codex", ATLVersion: "test", SkillActivation: activation}
		if err := runtime.validate(); err != nil {
			t.Fatalf("activation %q rejected: %v", activation, err)
		}
	}
	runtime := Runtime{Provider: "codex", ATLVersion: "test", SkillActivation: "future"}
	if err := runtime.validate(); err == nil {
		t.Fatal("unknown activation passed")
	}
}
