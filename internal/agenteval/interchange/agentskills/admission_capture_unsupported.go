//go:build !linux

package agentskills

const structuralAdmissionSupported = false

func openStructuralRoot(string) (structuralRoot, *structuralSourceRefusal) {
	return nil, &structuralSourceRefusal{
		code: FindingPlatformUnsupported, class: FindingPolicyRefusal, location: ".",
	}
}
