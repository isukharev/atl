// Command check-agent-eval-support validates the maintainer-owned standalone
// agent-eval support contour without contacting a provider, backend, or
// release service.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

type policy struct {
	Schema        string `json:"schema"`
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	SupportOwner  struct {
		Kind          string `json:"kind"`
		Repository    string `json:"repository"`
		SecurityRoute string `json:"security_route"`
	} `json:"support_owner"`
	ExternalConsumer struct {
		Kind          string `json:"kind"`
		Evidence      string `json:"evidence"`
		NamedConsumer bool   `json:"named_consumer"`
	} `json:"external_consumer"`
	Cadence struct {
		Stable     string `json:"stable"`
		PreRelease string `json:"pre_release"`
	} `json:"cadence"`
	Platforms []struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		State        string `json:"state"`
		Surface      string `json:"surface"`
	} `json:"platforms"`
	ExcludedPlatforms []struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Reason       string `json:"reason"`
	} `json:"excluded_platforms"`
	Components []struct {
		ID             string `json:"id"`
		State          string `json:"state"`
		ProviderAccess string `json:"provider_access"`
		BackendAccess  string `json:"backend_access"`
		Network        string `json:"network"`
		Identity       string `json:"identity"`
		Route          string `json:"route"`
	} `json:"components"`
	Compatibility struct {
		SchemaPolicy     string `json:"schema_policy"`
		FutureGeneration string `json:"future_generation"`
		ContractWindow   string `json:"contract_window"`
		Rollback         string `json:"rollback"`
	} `json:"compatibility"`
	Deprecation struct {
		NoticeDays     int    `json:"notice_days"`
		NoticeReleases int    `json:"notice_releases"`
		RemovalMajor   bool   `json:"removal_requires_major"`
		ClockStarts    string `json:"clock_starts"`
	} `json:"deprecation"`
	Security struct {
		ResponseRoute string `json:"response_route"`
		Target        string `json:"target"`
		AutoUpdates   bool   `json:"automatic_updates"`
	} `json:"security"`
	Release struct {
		PublicationAuthority string   `json:"publication_authority"`
		StablePrerequisites  []string `json:"stable_prerequisites"`
		RepositoryExtraction string   `json:"repository_extraction"`
	} `json:"release"`
}

func main() {
	data, err := os.ReadFile("docs/maintainers/agent-eval-support.v1.json")
	if err != nil {
		fail(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value policy
	if err := decoder.Decode(&value); err != nil {
		fail(fmt.Errorf("support policy is not valid JSON: %w", err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		fail(errors.New("support policy has trailing JSON"))
	}
	if err := validate(value); err != nil {
		fail(err)
	}
	docs, err := os.ReadFile("docs/maintainers/agent-eval-support.md")
	if err != nil {
		fail(err)
	}
	if !bytes.Contains(docs, []byte("agent-eval-support.v1.json")) || !bytes.Contains(docs, []byte("pre_release")) {
		fail(errors.New("support policy documentation is not bound to the machine policy"))
	}
	fmt.Println("agent-eval support policy: pre-release contour is closed and machine-bound")
}

func validate(value policy) error {
	if value.Schema != "agent-eval/support-policy" || value.SchemaVersion != 1 || value.Status != "pre_release" {
		return errors.New("support policy schema or status is invalid")
	}
	if value.SupportOwner.Kind != "repository_maintainer" || value.SupportOwner.Repository != "github.com/isukharev/atl" || value.SupportOwner.SecurityRoute != "SECURITY.md" {
		return errors.New("support owner route is invalid")
	}
	if value.ExternalConsumer.Kind != "out_of_tree_provider_free_conformance_runner" || value.ExternalConsumer.Evidence != "required_before_first_signed_release" || value.ExternalConsumer.NamedConsumer {
		return errors.New("support policy must keep external-consumer evidence pending")
	}
	if len(value.Platforms) != 1 || value.Platforms[0].OS != "linux" || value.Platforms[0].Architecture != "amd64" || value.Platforms[0].State != "candidate" || value.Platforms[0].Surface != "provider_free_process" {
		return errors.New("candidate platform contour is invalid")
	}
	if len(value.ExcludedPlatforms) != 3 {
		return errors.New("excluded platform contour is incomplete")
	}
	if value.Compatibility.FutureGeneration != "refuse" || value.Compatibility.ContractWindow != "pre_release_no_stable_clock" || value.Compatibility.Rollback != "required_before_stable_release" {
		return errors.New("compatibility policy is invalid")
	}
	if value.Deprecation.NoticeDays != 180 || value.Deprecation.NoticeReleases != 2 || !value.Deprecation.RemovalMajor || value.Deprecation.ClockStarts != "first_conforming_signed_standalone_release" {
		return errors.New("deprecation policy is invalid")
	}
	if value.Release.PublicationAuthority != "separate_explicit_approval" || value.Release.RepositoryExtraction != "not_authorized" || len(value.Release.StablePrerequisites) < 5 {
		return errors.New("release prerequisites are incomplete")
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
