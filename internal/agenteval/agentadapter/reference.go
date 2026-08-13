package agentadapter

const referenceAdapterID = "reference-deterministic"

// ReferenceContract returns the fixed in-process adapter used by the public
// provider-free reference composition. It observes one synthetic primary node
// and has no process, provider, network, credential, or configuration surface.
func ReferenceContract() (Contract, error) {
	support := make(map[CapabilityID]Support, len(closedCapabilities))
	for _, capability := range closedCapabilities {
		support[capability] = SupportNotApplicable
	}
	for _, capability := range []CapabilityID{
		CapabilityActivationEvidence,
		CapabilityActivationNative,
		CapabilitySingle,
		CapabilityTrajectory,
	} {
		support[capability] = SupportSupported
	}
	source, executable, configuration := referenceIdentities()
	return NewContract(referenceAdapterID, "1", source, executable, configuration, support, []ConfigurationKey{})
}

// NewReferenceObservation returns the canonical observation for one completed
// in-process reference action. Activation is observed only for a treatment
// whose immutable design expects the selected skill to be active.
func NewReferenceObservation(contract Contract, attemptID string, activated bool) (Observation, error) {
	want, err := ReferenceContract()
	if err != nil || !referenceContractMatches(contract) {
		return Observation{}, contractError("reference_contract")
	}
	wantSHA, digestErr := ContractSHA256(want)
	gotSHA, gotDigestErr := ContractSHA256(contract)
	if digestErr != nil || gotDigestErr != nil || wantSHA != gotSHA {
		return Observation{}, contractError("reference_contract")
	}
	activation := Activation{Mode: ActivationUnavailable, UseEvidence: UseEvidenceUnavailable}
	capabilities := []CapabilityID{CapabilitySingle, CapabilityTrajectory}
	if activated {
		activation = Activation{Mode: ActivationNative, UseEvidence: UseEvidenceObserved}
		capabilities = []CapabilityID{CapabilityActivationEvidence, CapabilityActivationNative, CapabilitySingle, CapabilityTrajectory}
	}
	events := []Event{
		{Sequence: 1, NodeID: "reference", Type: EventStart, Start: &Start{
			Role: RolePrimary, Capabilities: capabilities, Activation: activation,
		}},
		{Sequence: 2, NodeID: "reference", Type: EventTerminal, Terminal: &Terminal{
			State: TerminalSucceeded, Usage: referenceUsage(),
		}},
	}
	return Normalize(contract, attemptID, ProfileSingle, events)
}

func referenceIdentities() (string, string, string) {
	return hashDomain("reference-source", []byte("closed-in-memory-v1")),
		hashDomain("reference-executable", []byte("in-process-v1")),
		hashDomain("reference-configuration", []byte("single+native-or-unavailable/v1"))
}

func referenceContractMatches(contract Contract) bool {
	if contract.AdapterID != referenceAdapterID || contract.AdapterVersion != "1" || len(contract.ConfigurationKeys) != 0 {
		return false
	}
	source, executable, configuration := referenceIdentities()
	if contract.SourceSHA256 != source || contract.ExecutableSHA256 != executable || contract.ConfigurationSHA256 != configuration ||
		len(contract.Capabilities) != len(closedCapabilities) {
		return false
	}
	for index, capability := range closedCapabilities {
		want := SupportNotApplicable
		switch capability {
		case CapabilityActivationEvidence, CapabilityActivationNative, CapabilitySingle, CapabilityTrajectory:
			want = SupportSupported
		}
		if contract.Capabilities[index] != (Capability{ID: capability, Support: want}) {
			return false
		}
	}
	return true
}

func referenceUsage() Usage {
	return Usage{
		EstimatedCostMicroUSD: NotApplicableMetric(),
		InputTokens:           NotApplicableMetric(),
		OutputTokens:          NotApplicableMetric(),
		ToolCalls:             NotApplicableMetric(),
		EvidenceItems:         NotApplicableMetric(),
	}
}
