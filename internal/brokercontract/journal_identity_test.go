package brokercontract

import (
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerJournalIdentityKnownAnswerVectors(t *testing.T) {
	context := fixtureContext()
	owner, err := BrokerJournalOwnerV1(context)
	execution, revision, writerErr := BrokerJournalWriterSHA256V1(context)
	wantOwner := domain.BrokerJournalOwner{
		BrokerSHA256:    "78afefc35ec759ba18a7b3bcca5fc2aface60be9e3d5e77ca5b1a17fbad9c51b",
		BackendSHA256:   "e4289e11072c4642a6a8ff1b2f78acae77d02d8bd928904763aa69d2be9f0798",
		PrincipalSHA256: "c484a2fc82b3a9d8a17990369496da41f05390956723e18f03c55b60a711490d",
		WorkloadSHA256:  "986abff678fe32f683565f0d9bc262d2c50434e22a03827b0d13c040a6d749f4",
		AudienceSHA256:  "b2c4be597b4eccfdeccd26270583115429c9154aa452918681696a3a2f8fb66e",
	}
	broker, brokerErr := BrokerJournalBrokerSHA256V1(context.BrokerID)
	if brokerErr != nil || broker != wantOwner.BrokerSHA256 {
		t.Fatalf("operator Broker identity=%q err=%v", broker, brokerErr)
	}
	if err != nil || writerErr != nil || owner != wantOwner || execution != "5a77d92cd4ccd4bc33720665f0a56eacc7cf35948c02b5b29f95f8886cc7d8d6" || revision != "e1890e13db30f155c453cc350cb29c07f23263451876799c5e73b2ee6d0ff916" {
		t.Fatalf("owner=%+v execution=%q revision=%q errors=%v/%v", owner, execution, revision, err, writerErr)
	}

	resource, err := BrokerOperationObservationResourceV1("ticket-1")
	wantResource := domain.BrokerQualifiedResource{
		Kind: domain.BrokerResourceOperation, ImmutableID: "ticket-1", AncestorIDs: []string{},
		VersionEvidence:  "7eb923ba6821b69d3147410fe817674ef756a08998d60d54619ea2e121cd8c4f",
		ProjectionSHA256: "1ef6ae31f906a6312c56de569f831d56d979956b05aed883028cc2fd3d934e1f",
	}
	if err != nil || !reflect.DeepEqual(resource, wantResource) {
		t.Fatalf("resource=%+v err=%v", resource, err)
	}
}

func TestBrokerJournalOwnerSeparatesStableOwnerFromWriter(t *testing.T) {
	base := fixtureContext()
	want, err := BrokerJournalOwnerV1(base)
	if err != nil {
		t.Fatal(err)
	}
	changedWriter := base
	changedWriter.ExecutionID = "execution-2"
	changedWriter.ExecutionEpoch = "epoch-2"
	changedWriter.AuthorityRevision = "revision-2"
	got, err := BrokerJournalOwnerV1(changedWriter)
	if err != nil || got != want {
		t.Fatalf("writer changed stable owner: %+v / %+v err=%v", got, want, err)
	}

	changes := []func(*domain.BrokerVerifiedContext){
		func(v *domain.BrokerVerifiedContext) { v.BrokerID = "broker-2" },
		func(v *domain.BrokerVerifiedContext) { v.Backend.WorkloadBackendID = "jira-secondary" },
		func(v *domain.BrokerVerifiedContext) { v.PrincipalID = "principal-2" },
		func(v *domain.BrokerVerifiedContext) { v.WorkloadID = "workload-2" },
		func(v *domain.BrokerVerifiedContext) { v.Audience = "other-audience" },
	}
	for index, change := range changes {
		current := base
		change(&current)
		got, changeErr := BrokerJournalOwnerV1(current)
		if changeErr != nil || got == want {
			t.Fatalf("change %d retained owner: %+v err=%v", index, got, changeErr)
		}
	}
}

func TestBrokerJournalIdentityRejectsMalformedInputs(t *testing.T) {
	for _, id := range []string{"", "broker\n", " broker", "broker "} {
		if _, err := BrokerJournalBrokerSHA256V1(id); err == nil {
			t.Fatal("malformed operator identity produced journal digest")
		}
	}
	context := fixtureContext()
	context.Backend.OriginSHA256 = "not-a-digest"
	if _, err := BrokerJournalOwnerV1(context); err == nil {
		t.Fatal("malformed context produced owner")
	}
	if _, _, err := BrokerJournalWriterSHA256V1(context); err == nil {
		t.Fatal("malformed context produced writer digests")
	}
	if _, err := BrokerOperationObservationResourceV1(""); err == nil {
		t.Fatal("empty ticket produced operation resource")
	}
}
