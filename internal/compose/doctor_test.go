package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func TestDoctorDependenciesValidateOnlySelectedServiceCABundles(t *testing.T) {
	configDir := t.TempDir()
	confluenceCA := filepath.Join(configDir, "unreadable-confluence.pem")
	jiraCA := filepath.Join(configDir, "jira.pem")
	configBody := []byte(`{"transport":{"confluence":{"ca_bundle":` + strconv.Quote(confluenceCA) + `},"jira":{"ca_bundle":` + strconv.Quote(jiraCA) + `}}}`)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), configBody, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_CONFIG_DIR", configDir)
	t.Setenv("ATL_CONFLUENCE_URL", "http://127.0.0.1:1")
	t.Setenv("ATL_CONFLUENCE_PAT", "confluence-test-token")
	t.Setenv("ATL_JIRA_URL", "http://127.0.0.1:1")
	t.Setenv("ATL_JIRA_PAT", "jira-test-token")

	var validated []string
	validator := func(path string) error {
		validated = append(validated, path)
		if path == confluenceCA {
			return errors.New("invalid test CA bundle")
		}
		return nil
	}

	jiraDependencies := doctorDependencies(app.DoctorServiceJira, validator)
	if !reflect.DeepEqual(validated, []string{jiraCA}) {
		t.Fatalf("jira validation paths=%v want only %q", validated, jiraCA)
	}
	if got := jiraDependencies.Config.Transport.Confluence; got.Status != "not_selected" || got.Source != "config_file" || !got.Configured || got.Reason != "" {
		t.Fatalf("unselected Confluence transport=%+v", got)
	}
	jiraResult, err := app.RunDoctor(context.Background(), app.DoctorOptions{
		Service: app.DoctorServiceJira, Dependencies: jiraDependencies,
	})
	if err != nil || !jiraResult.Healthy || !jiraResult.Complete {
		t.Fatalf("jira result=%+v err=%v", jiraResult, err)
	}

	validated = nil
	allDependencies := doctorDependencies(app.DoctorServiceAll, validator)
	if !reflect.DeepEqual(validated, []string{confluenceCA, jiraCA}) {
		t.Fatalf("all validation paths=%v", validated)
	}
	allResult, err := app.RunDoctor(context.Background(), app.DoctorOptions{
		Service: app.DoctorServiceAll, Dependencies: allDependencies,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || allResult.Healthy || !allResult.Complete ||
		allResult.Config.Transport.Confluence.Status != "invalid" {
		t.Fatalf("all result=%+v err=%v", allResult, err)
	}
}
